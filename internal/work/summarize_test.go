package work

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

type fakeOneShot struct {
	mu      sync.Mutex
	outputs []string // one per call; the last repeats
	calls   []OneShotRequest
	err     error
	checkEr error
}

func (f *fakeOneShot) Check(string) error { return f.checkEr }
func (f *fakeOneShot) Run(_ context.Context, r OneShotRequest) (OneShotResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
	if f.err != nil {
		return OneShotResult{}, f.err
	}
	i := len(f.calls) - 1
	if i >= len(f.outputs) {
		i = len(f.outputs) - 1
	}
	return OneShotResult{JobID: "job-sum-" + string(rune('a'+len(f.calls))), Output: f.outputs[i]}, nil
}
func (f *fakeOneShot) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type fakeTranscripts struct {
	data []byte
	err  error
}

func (f fakeTranscripts) ReadTail(context.Context, jobstore.AgentSession, int64) ([]byte, error) {
	return f.data, f.err
}

const goodJSON = `{"goal":"给订单页加导出","progress":"按钮已加好","blocker_kind":"person","blocker":"等后端接口","next":"联调","status_hint":"waiting_resource","confidence":0.8}`

const claudeTail = `{"type":"user","message":{"role":"user","content":"给订单页加导出按钮"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"按钮已加好，等后端接口。"}]}}
`

// testClock drives both the store and the service so "idle for N minutes" is exact.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Set(t time.Time)         { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *testClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var clocks sync.Map

func clockOf(svc *Service) *testClock { v, _ := clocks.Load(svc); return v.(*testClock) }

var clockT0 = time.Date(2026, 10, 6, 10, 0, 0, 0, time.Local)

func sumFixture(t *testing.T, outputs ...string) (*Service, *jobstore.Store, jobstore.WorkItem, *fakeOneShot) {
	t.Helper()
	svc, st, _ := newSvc(t)
	clk := &testClock{t: clockT0}
	st.SetClock(clk.Now)
	svc.SetNow(clk.Now)
	clocks.Store(svc, clk)
	a, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-sum00001", Agent: "claude", ProjectKey: "p1", Cwd: "/ws",
		State: jobstore.SessionIdle, Transcript: "/home/x/.claude/projects/y/sess.jsonl"})
	assert.NoErr(t, err)
	_, _, err = st.TouchAgentSession(a.SessionID, jobstore.SessionHeartbeat{State: jobstore.SessionIdle, LastMessage: "按钮已加好"})
	assert.NoErr(t, err)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "订单导出", SessionIDs: []string{a.SessionID}, By: "human:alice"})
	assert.NoErr(t, err)
	os := &fakeOneShot{outputs: outputs}
	svc.SetOneShot(os)
	svc.SetTranscriptSource(fakeTranscripts{data: []byte(claudeTail)})
	return svc, st, w, os
}

func TestParseSummaryVariants(t *testing.T) {
	cases := map[string]string{
		"plain":    goodJSON,
		"fenced":   "```json\n" + goodJSON + "\n```",
		"chatter":  "好的，结果如下：\n" + goodJSON + "\n希望有帮助",
		"envelope": `{"type":"system","subtype":"init"}` + "\n" + `{"type":"result","result":"` + strings.ReplaceAll(goodJSON, `"`, `\"`) + `"}`,
	}
	for name, in := range cases {
		o, err := ParseSummary(in)
		assert.NoErr(t, err, name)
		assert.Eq(t, "给订单页加导出", o.Goal, name)
		assert.Eq(t, "按钮已加好", o.Progress, name)
		assert.Eq(t, "waiting_resource", o.StatusHint, name)
		assert.Eq(t, 0.8, o.Confidence, name)
	}
	o, err := ParseSummary(`{"goal":"g","summary":"s","confidence":"0.5"}`)
	assert.NoErr(t, err)
	assert.Eq(t, "s", o.Progress)
	assert.Eq(t, 0.5, o.Confidence)
	_, err = ParseSummary("没有任何 JSON")
	assert.Err(t, err)
	_, err = ParseSummary(`{"unrelated":1}`)
	assert.Err(t, err)
}

func TestSummarizeFillsEmptyFieldsAndKeepsHumanOnesAsSuggestions(t *testing.T) {
	svc, st, w, os := sumFixture(t, goodJSON)
	// A person wrote the goal; the blocker is still empty.
	goal := "人写的目标"
	_, err := svc.Update(w.ID, jobstore.WorkItemPatch{Goal: &goal}, 0, "human:alice")
	assert.NoErr(t, err)

	res, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err)
	assert.False(t, res.Degraded)
	assert.Eq(t, 1, os.callCount())
	assert.True(t, strings.Contains(os.calls[0].Prompt, "按钮已加好，等后端接口"))
	assert.Eq(t, "claude", os.calls[0].Agent)

	got, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, "人写的目标", got.Goal) // never overwritten
	assert.Eq(t, "等后端接口", got.BlockerText)
	assert.Eq(t, "person", got.BlockerKind)
	assert.Eq(t, "联调", got.NextStep)
	assert.Eq(t, "按钮已加好", got.Summary)
	assert.Eq(t, jobstore.WorkActive, got.Status) // the status is only a hint
	assert.Eq(t, []string{"下一步", "摘要", "阻塞"}, sortedCopy(res.Applied))

	srcs, _ := st.WorkFieldSources(w.ID)
	assert.Eq(t, "summarizer(claude)", srcs[jobstore.WorkFieldBlocker].By)
	assert.Eq(t, "human:alice", srcs[jobstore.WorkFieldGoal].By)

	sugg, _ := st.ListWorkSuggestions(w.ID)
	byField := map[string]string{}
	for _, s := range sugg {
		byField[s.Field] = s.Value
	}
	assert.Eq(t, "给订单页加导出", byField[jobstore.SuggestGoal])
	assert.Eq(t, "waiting_resource", byField[jobstore.SuggestStatusHint])
	assert.Eq(t, "steward", mustLastJournalKind(t, st, w.ID))

	// Accept the goal: it becomes the person's write; dismiss the status hint.
	_, err = svc.AcceptSuggestion(w.ID, jobstore.SuggestGoal, "human:alice")
	assert.NoErr(t, err)
	got, _, _ = st.GetWorkItem(w.ID)
	assert.Eq(t, "给订单页加导出", got.Goal)
	srcs, _ = st.WorkFieldSources(w.ID)
	assert.Eq(t, "human:alice", srcs[jobstore.WorkFieldGoal].By)
	assert.NoErr(t, svc.DismissSuggestion(w.ID, jobstore.SuggestStatusHint, "human:alice"))
	assert.True(t, errors.Is(svc.DismissSuggestion(w.ID, jobstore.SuggestStatusHint, "human:alice"), jobstore.ErrSuggestionNotFound))

	// A second run with a changed answer refreshes the summarizer-owned fields only;
	// the dismissed status hint with the same value is not proposed again.
	os.outputs = []string{`{"goal":"别的目标","progress":"联调完成一半","blocker":"","next":"回归测试","status_hint":"waiting_resource","confidence":0.9}`}
	res, err = svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err)
	got, _, _ = st.GetWorkItem(w.ID)
	assert.Eq(t, "联调完成一半", got.Summary)
	assert.Eq(t, "回归测试", got.NextStep)
	assert.Eq(t, "", got.BlockerText) // the summarizer's own blocker cleared
	assert.Eq(t, "给订单页加导出", got.Goal)
	sugg, _ = st.ListWorkSuggestions(w.ID)
	for _, s := range sugg {
		assert.NotEq(t, jobstore.SuggestStatusHint, s.Field)
	}
	// Goal now differs from the person's adopted goal: a suggestion again, not a write.
	assert.True(t, len(sugg) == 1 && sugg[0].Field == jobstore.SuggestGoal && sugg[0].Value == "别的目标")
}

func TestAcceptStatusHintSetsHumanStatus(t *testing.T) {
	svc, st, w, _ := sumFixture(t, goodJSON)
	_, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err)
	got, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkActive, got.Status)
	_, err = svc.AcceptSuggestion(w.ID, jobstore.SuggestStatusHint, "human:alice")
	assert.NoErr(t, err)
	got, _, _ = st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkWaitingResource, got.Status)
	assert.Eq(t, jobstore.WorkSourceHuman, got.StatusSource)
	_, err = svc.AcceptSuggestion(w.ID, jobstore.SuggestStatusHint, "human:alice")
	assert.True(t, errors.Is(err, jobstore.ErrSuggestionNotFound))
}

func TestSummarizeRetriesOnceThenFails(t *testing.T) {
	svc, st, w, os := sumFixture(t, "不是 JSON", goodJSON)
	res, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err)
	assert.Eq(t, 2, os.callCount())
	assert.True(t, strings.Contains(os.calls[1].Prompt, "不是合法的 JSON"))
	assert.True(t, len(res.Applied) > 0)

	svc2, st2, w2, os2 := sumFixture(t, "还是不是 JSON")
	req, _ := st2.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: w2.ID, Kind: jobstore.WorkRequestSummarize})
	_, err = svc2.RunSummarize(context.Background(), w2.ID, SummarizeOpts{Cause: CauseRequest, RequestID: req.ID})
	assert.Err(t, err)
	assert.Eq(t, 2, os2.callCount())
	r, _, _ := st2.GetWorkRequest(req.ID)
	assert.Eq(t, jobstore.WorkRequestFailed, r.State)
	runs, _ := st2.ListWorkSummaries(w2.ID, 5)
	assert.Eq(t, jobstore.SummaryFailed, runs[0].State)
	_ = st
}

func TestSummarizeDegradesWithoutTranscriptAndSettlesRequest(t *testing.T) {
	svc, st, w, os := sumFixture(t, goodJSON)
	svc.SetTranscriptSource(fakeTranscripts{err: ErrNoTranscript})
	req, _ := st.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: w.ID, Kind: jobstore.WorkRequestSummarize})
	res, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseRequest, RequestID: req.ID})
	assert.NoErr(t, err)
	assert.True(t, res.Degraded)
	assert.True(t, strings.Contains(os.calls[0].Prompt, "会话最后一条消息"))
	r, _, _ := st.GetWorkRequest(req.ID)
	assert.Eq(t, jobstore.WorkRequestAnswered, r.State)
}

func TestSummarizeUnavailableAgentAndDailyLimit(t *testing.T) {
	svc, st, w, os := sumFixture(t, goodJSON)
	os.checkEr = errors.New("agent claude 不存在")
	req, _ := st.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: w.ID, Kind: jobstore.WorkRequestSummarize})
	_, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseRequest, RequestID: req.ID})
	assert.Err(t, err)
	r, _, _ := st.GetWorkRequest(req.ID)
	assert.Eq(t, jobstore.WorkRequestFailed, r.State)
	assert.True(t, strings.Contains(r.Error, "agent claude 不存在"))
	assert.False(t, svc.SummarizerStatus().Available)
	_, err = svc.TidyNow(w.ID, "human:alice")
	assert.True(t, errors.Is(err, ErrSummarizerUnavailable))

	os.checkEr = nil
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{SummarizeDailyLimit: 1} })
	_, err = svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseAuto})
	assert.NoErr(t, err)
	_, err = svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseAuto})
	assert.Err(t, err) // cap reached
	_, err = svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err) // the manual action ignores the cap
	assert.Eq(t, 1, svc.SummarizerStatus().DailyUsed)
}

func TestTidyNowRunsInBackgroundAndAnswersRequest(t *testing.T) {
	svc, st, w, _ := sumFixture(t, goodJSON)
	req, err := svc.TidyNow(w.ID, "human:alice")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkRequestSummarize, req.Kind)
	svc.WaitIdle()
	r, _, _ := st.GetWorkRequest(req.ID)
	assert.Eq(t, jobstore.WorkRequestAnswered, r.State)
	got, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, "联调", got.NextStep)
}

func TestScanSummarizeTriggersAndCostControl(t *testing.T) {
	svc, st, _, os := sumFixture(t, goodJSON)
	clk := clockOf(svc)
	scan := func() { svc.scanSummarize(svc.nowFn()); svc.WaitIdle() }
	touch := func(state string) {
		_, _, err := st.TouchAgentSession("sess-sum00001", jobstore.SessionHeartbeat{State: state, LastMessage: "又做了点"})
		assert.NoErr(t, err)
	}

	clk.Advance(5 * time.Minute)
	scan() // idle only 5 min: not yet
	assert.Eq(t, 0, os.callCount())

	clk.Advance(11 * time.Minute) // 16 min idle
	scan()
	assert.Eq(t, 1, os.callCount())

	// No new activity since: never again, however long it idles.
	clk.Advance(5 * time.Hour)
	scan()
	assert.Eq(t, 1, os.callCount())

	// New activity, but inside the 30-minute per-session gap: wait; past it and idle again: run.
	touch(jobstore.SessionIdle)
	clk.Advance(20 * time.Minute)
	scan()
	assert.Eq(t, 2, os.callCount()) // the previous run was 5h ago, so the gap is long past
	touch(jobstore.SessionIdle)
	clk.Advance(20 * time.Minute) // idle ok, but the last run was only 20 min ago
	scan()
	assert.Eq(t, 2, os.callCount())
	clk.Advance(15 * time.Minute)
	scan()
	assert.Eq(t, 3, os.callCount())

	// A running session is never tidied; an ended one with new activity is, once.
	touch(jobstore.SessionRunning)
	clk.Advance(6 * time.Hour)
	scan()
	assert.Eq(t, 3, os.callCount())
	touch(jobstore.SessionEnded)
	clk.Advance(time.Minute)
	scan()
	assert.Eq(t, 4, os.callCount())
	scan()
	assert.Eq(t, 4, os.callCount())
}

func TestScanSummarizeDisabledOrUnavailable(t *testing.T) {
	svc, _, _, os := sumFixture(t, goodJSON)
	clockOf(svc).Advance(time.Hour)
	svc.SetConfigFn(func() config.WorkConfig { f := false; return config.WorkConfig{SummarizeEnabled: &f} })
	svc.scanSummarize(svc.nowFn())
	svc.WaitIdle()
	assert.Eq(t, 0, os.callCount())
	svc.SetConfigFn(nil)
	os.checkEr = errors.New("x")
	svc.scanSummarize(svc.nowFn())
	svc.WaitIdle()
	assert.Eq(t, 0, os.callCount())
}

func TestSessionClaimIsExclusive(t *testing.T) {
	svc, _, _ := newSvc(t)
	assert.True(t, svc.claimSession("s"))
	assert.False(t, svc.claimSession("s"))
	svc.releaseSession("s")
	assert.True(t, svc.claimSession("s"))
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func mustLastJournalKind(t *testing.T, st *jobstore.Store, id string) string {
	t.Helper()
	j, err := st.ListWorkJournal(id, 50, 0)
	assert.NoErr(t, err)
	return j[len(j)-1].Kind
}

// choosyOneShot adds the ProjectChooser face: only the listed projects admit the agent.
type choosyOneShot struct {
	*fakeOneShot
	usable map[string]bool
	dirs   map[string]string
}

func (c choosyOneShot) ProjectUsable(key, _ string) bool { return c.usable[key] }
func (c choosyOneShot) ProjectDir(key string) string     { return c.dirs[key] }

// TestSummarizerProjectResolutionThreeTiers pins the order: work.summarizer_project →
// the item's / session's own project (when it admits the agent) → the default project.
func TestSummarizerProjectResolutionThreeTiers(t *testing.T) {
	run := func(t *testing.T, summarizerProject string, usable map[string]bool) (OneShotRequest, SummarizerStatus) {
		t.Helper()
		svc, _, w, fake := sumFixture(t, goodJSON)
		svc.SetOneShot(choosyOneShot{fakeOneShot: fake, usable: usable, dirs: map[string]string{"default": "/home/u/.gofer/workspace", "p1": "/ws"}})
		svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{SummarizerProject: summarizerProject} })
		if _, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual}); err != nil {
			t.Fatal(err)
		}
		if fake.callCount() != 1 {
			t.Fatalf("calls = %d", fake.callCount())
		}
		return fake.calls[0], svc.SummarizerStatus()
	}

	// 1. the configured project wins even when the session's own project is usable.
	req, st := run(t, "pinned", map[string]bool{"p1": true, "default": true, "pinned": true})
	assert.Eq(t, "pinned", req.ProjectKey)
	assert.Eq(t, ProjectSourceConfig, st.ProjectSource)
	assert.Eq(t, "pinned", st.EffectiveProject)

	// 2. unset: the session's own project when it admits the agent and the local runner.
	req, st = run(t, "", map[string]bool{"p1": true, "default": true})
	assert.Eq(t, "p1", req.ProjectKey)
	// the status has no item: it shows where an item-less job lands.
	assert.Eq(t, ProjectSourceDefault, st.ProjectSource)
	assert.Eq(t, "default", st.EffectiveProject)
	assert.Eq(t, "/home/u/.gofer/workspace", st.EffectiveDir)

	// 3. unset and the item's project is not usable: fall back to default.
	req, _ = run(t, "", map[string]bool{"default": true})
	assert.Eq(t, "default", req.ProjectKey)
}

func TestSummarizerStatusFlagsUnusableProject(t *testing.T) {
	svc, _, _, fake := sumFixture(t, goodJSON)
	svc.SetOneShot(choosyOneShot{fakeOneShot: fake, usable: map[string]bool{}})
	st := svc.SummarizerStatus()
	assert.False(t, st.Available)
	assert.True(t, strings.Contains(st.Reason, "default"))
	svc.SetOneShot(choosyOneShot{fakeOneShot: fake, usable: map[string]bool{"default": true}})
	assert.True(t, svc.SummarizerStatus().Available)
}

type dialectTranscripts struct {
	fakeTranscripts
	dialect string
}

func (d dialectTranscripts) ConfiguredDialect(string) string { return d.dialect }

// A session's configured transcript_dialect beats the agent-name guess: the fixture's
// agent is "claude" (name guess = claude dialect), yet the file is generic jsonl.
func TestSummarizeUsesConfiguredTranscriptDialect(t *testing.T) {
	svc, _, w, os := sumFixture(t, goodJSON)
	generic := `{"v":1,"type":"user","text":"给订单页加导出按钮"}` + "\n" + `{"v":1,"type":"assistant","text":"按钮已加好，等后端接口。"}` + "\n"
	svc.SetTranscriptSource(dialectTranscripts{fakeTranscripts{data: []byte(generic)}, "generic"})
	res, err := svc.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseManual})
	assert.NoErr(t, err)
	assert.False(t, res.Degraded)
	assert.True(t, strings.Contains(os.calls[0].Prompt, "按钮已加好，等后端接口"))
}
