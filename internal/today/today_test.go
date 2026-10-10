package today

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

var testNow = time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local)

func newTestService(t *testing.T) (*Service, *jobstore.Store) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "today.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.SetClock(func() time.Time { return testNow })
	ws := work.New(st)
	t.Cleanup(func() { _ = ws.Close() }) // before the store closes (gofer-r7am)
	ws.SetNow(func() time.Time { return testNow })
	svc := New(Deps{
		Store: st, Work: ws, Now: func() time.Time { return testNow },
		Runners: func() RunnerStatus { return RunnerStatus{Online: 1, Total: 2, Offline: []string{"w-mac"}} },
		Version: func() string { return "v0.127.0" },
	})
	return svc, st
}

func ago(min int) int64 { return testNow.Unix() - int64(min)*60 }

func putJob(t *testing.T, st *jobstore.Store, rec jobstore.JobRecord) {
	t.Helper()
	if rec.RequestJSON == "" {
		rec.RequestJSON = `{"title":"job ` + rec.ID + `"}`
	}
	if rec.StartedAt == 0 {
		rec.StartedAt = ago(60)
	}
	if rec.UpdatedAt == 0 {
		rec.UpdatedAt = rec.StartedAt
	}
	assert.NoErr(t, st.UpsertJob(rec))
}

func keys(cards []Card) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.Key)
	}
	return out
}

func find(cards []Card, key string) (Card, bool) {
	for _, c := range cards {
		if c.Key == key {
			return c, true
		}
	}
	return Card{}, false
}

func TestDecisionsInclusionAndExecExclusion(t *testing.T) {
	svc, st := newTestService(t)
	// needs_review: one codex job, one exec job.
	putJob(t, st, jobstore.JobRecord{ID: "j-rev", ProjectKey: "p1", Agent: "codex", Status: job.StatusNeedsReview, EndedAt: ago(30),
		DiffSummary: " 3 files changed, 214 insertions(+), 37 deletions(-)", CommitsJSON: `[{"sha":"a"},{"sha":"b"}]`,
		VerifyJSON: `{"status":"passed"}`, ResultJSON: `{"summary":"改为逐行写出\n\n第二段"}`})
	putJob(t, st, jobstore.JobRecord{ID: "j-exec", ProjectKey: "p1", Agent: "exec", Status: job.StatusNeedsReview, EndedAt: ago(20)})
	// pure information: a finished job never becomes a card.
	putJob(t, st, jobstore.JobRecord{ID: "j-done", ProjectKey: "p1", Agent: "codex", Status: job.StatusDone, EndedAt: ago(5)})

	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"review:j-rev"}, keys(cards))
	rev := cards[0]
	assert.Eq(t, "待验收", rev.Tag)
	assert.Eq(t, "改为逐行写出", rev.Summary)
	assert.Eq(t, Review{Commits: 2, Adds: 214, Dels: 37, Verify: "passed"}, *rev.Review)
	assert.Nil(t, rev.Advice)
	assert.Eq(t, []string{"accept", "rerun", "diff"}, actionIDs(rev.Actions))

	cards, err = svc.Decisions(true)
	assert.NoErr(t, err)
	assert.Len(t, cards, 2)
}

func actionIDs(actions []Action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.ID)
	}
	return out
}

func TestScoringAndOrdering(t *testing.T) {
	svc, st := newTestService(t)
	// A plan whose todo t1 failed: t2 and t3 (transitively) wait on it; t4 is done.
	assert.NoErr(t, st.InsertPlan(jobstore.Plan{PlanID: "p-1", Title: "导出改造", Status: jobstore.PlanBlocked, BlockedTodo: "t1", ProjectKey: "p1"}))
	assert.NoErr(t, st.InsertTodo(jobstore.PlanTodo{TodoID: "t1", PlanID: "p-1", Title: "后端", Status: jobstore.TodoDoing, DispatchError: "Windows 测试失败"}))
	assert.NoErr(t, st.InsertTodo(jobstore.PlanTodo{TodoID: "t2", PlanID: "p-1", Title: "前端", After: []string{"t1"}}))
	assert.NoErr(t, st.InsertTodo(jobstore.PlanTodo{TodoID: "t3", PlanID: "p-1", Title: "文档", After: []string{"t2"}}))
	assert.NoErr(t, st.InsertTodo(jobstore.PlanTodo{TodoID: "t4", PlanID: "p-1", Title: "旧", After: []string{"t1"}, Status: jobstore.TodoDone}))
	assert.NoErr(t, st.SetPlanBlocked("p-1", "t1"))

	// A running job held 25 minutes by a tool approval, in a worktree.
	putJob(t, st, jobstore.JobRecord{ID: "j-run", ProjectKey: "p1", Agent: "codex", Status: job.StatusRunning, WorktreePath: "/wt/1"})
	assert.NoErr(t, st.UpsertInteraction(jobstore.InteractionRecord{ID: "i1", JobID: "j-run", Type: job.InteractionTypePermission,
		Prompt: "go test ./...", Status: "pending", CreatedAt: ago(25),
		OptionsJSON:  `[{"value":"a1","label":"允许","kind":"allow_once"},{"value":"a2","label":"总是允许","kind":"allow_always"},{"value":"r1","label":"拒绝","kind":"reject_once"}]`,
		ToolCallJSON: `{"id":"tc","title":"执行命令"}`}))
	// An interaction about to time out.
	putJob(t, st, jobstore.JobRecord{ID: "j-hot", ProjectKey: "p1", Agent: "claude", Status: job.StatusRunning})
	assert.NoErr(t, st.UpsertInteraction(jobstore.InteractionRecord{ID: "i2", JobID: "j-hot", Type: job.InteractionTypeQuestion,
		Prompt: "继续吗？", Status: "pending", CreatedAt: ago(2), ExpiresAt: testNow.Unix() + 300}))
	// An old review job with nothing waiting on it.
	putJob(t, st, jobstore.JobRecord{ID: "j-rev", ProjectKey: "p1", Agent: "codex", Status: job.StatusNeedsReview, EndedAt: ago(300)})

	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"interaction:j-hot/i2", "plan_blocked:p-1", "interaction:j-run/i1", "review:j-rev"}, keys(cards))

	hot := cards[0]
	assert.Eq(t, UrgencyNow, hot.Urgency)
	// No 「交给管家」: punt only marks needs_human, and the card is already a person's.
	assert.Eq(t, []string{"reply"}, actionIDs(hot.Actions))

	plan := cards[1]
	assert.Eq(t, UrgencyBlocking, plan.Urgency)
	assert.Eq(t, Blocks{Score: 6, Items: 2, Text: "plan 导出改造 后面 2 项在等"}, plan.Blocks)
	assert.Eq(t, "导出改造 · 后端", plan.Title)
	assert.Eq(t, "Windows 测试失败", plan.Summary)
	assert.Eq(t, []string{"resume", "open"}, actionIDs(plan.Actions))

	perm := cards[2]
	assert.Eq(t, "工具审批", perm.Tag)
	assert.Eq(t, "codex 请求执行命令", perm.Title)
	// 2 + 25min/10 = 4 for the held session, +1 for the worktree.
	assert.Eq(t, 5, perm.Blocks.Score)
	assert.Eq(t, 2, perm.Blocks.Items)
	assert.StrContains(t, perm.Blocks.Text, "占着 codex 会话 25 分钟")
	assert.StrContains(t, perm.Blocks.Text, "占着 worktree")
	// allow_once / reject_once first, then the other options (up to 3).
	assert.Eq(t, []string{"a1", "r1", "a2"}, []string{perm.Actions[0].Value, perm.Actions[1].Value, perm.Actions[2].Value})
	assert.Eq(t, []string{"answer", "answer", "answer"}, actionIDs(perm.Actions))

	assert.Eq(t, UrgencyNormal, cards[3].Urgency)
}

func TestAgentScoreCap(t *testing.T) {
	var b blockSet
	b.agent("codex", "会话", 10*3600)
	assert.Eq(t, scoreAgentCap, b.result().Score)
}

func TestRelayOneCardPerSessionAndAckedSkipped(t *testing.T) {
	svc, st := newTestService(t)
	_, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "s1", Agent: "claude", ProjectKey: "p1", Title: "发版前确认",
		State: jobstore.SessionWaitingReply})
	assert.NoErr(t, err)
	_, _, err = st.TouchAgentSession("s1", jobstore.SessionHeartbeat{LastMessage: "是否 push？"})
	assert.NoErr(t, err)
	for _, id := range []string{"d1", "d2"} {
		assert.NoErr(t, st.InsertDecision(&jobstore.PlanDecision{ID: id, Title: "turn", Question: "q " + id, Kind: jobstore.DecisionKindRelay,
			SessionID: "s1", AskedAt: ago(10), TimeoutSec: 86400}))
	}
	// An acknowledged turn of another session is not pending.
	assert.NoErr(t, st.InsertDecision(&jobstore.PlanDecision{ID: "d3", Title: "turn", Question: "q", Kind: jobstore.DecisionKindRelay,
		SessionID: "s2", AskedAt: ago(10), TimeoutSec: 86400}))
	_, err = st.AckDecision("d3", "me")
	assert.NoErr(t, err)
	// A plain global question.
	assert.NoErr(t, st.InsertDecision(&jobstore.PlanDecision{ID: "d4", Title: "分页方式", Question: "A 还是 B？", OptionsJSON: `["A","B"]`,
		AskedAt: ago(5), TimeoutSec: 86400}))

	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"relay:s1", "decision:d4"}, keys(cards))
	relay := cards[0]
	assert.Eq(t, "发版前确认", relay.Title)
	assert.Eq(t, "是否 push？", relay.Summary)
	assert.Eq(t, "r:s1", relay.Refs.ThreadID)
	// 「已读」 acks every unread turn the card stands for.
	assert.Eq(t, []string{"d1", "d2"}, relay.Refs.DecisionIDs)
	assert.Eq(t, []string{"reply", "ack"}, actionIDs(relay.Actions))
	assert.Eq(t, 3, relay.Blocks.Score)
	dec := cards[1]
	assert.Eq(t, []string{"A", "B"}, []string{dec.Actions[0].Value, dec.Actions[1].Value})
}

func TestWorkAndSuggestionMerge(t *testing.T) {
	svc, st := newTestService(t)
	_, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "s-w", Agent: "claude", ProjectKey: "p1", State: jobstore.SessionIdle})
	assert.NoErr(t, err)
	w1, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "外部页面", Status: jobstore.WorkNeedsMe, SessionIDs: []string{"s-w"}, By: "me"})
	assert.NoErr(t, err)
	w2, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "用量统计", Status: jobstore.WorkActive, By: "me"})
	assert.NoErr(t, err)
	w3, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "无关", Status: jobstore.WorkActive, By: "me"})
	assert.NoErr(t, err)
	for _, sg := range []jobstore.WorkSuggestion{
		{WorkItemID: w1.ID, Field: jobstore.SuggestGoal, Value: "支持 iframe", By: "summarizer"},
		{WorkItemID: w2.ID, Field: jobstore.SuggestStatusHint, Value: jobstore.WorkReview, By: "summarizer"},
		// next suggestions stay in Works.
		{WorkItemID: w3.ID, Field: jobstore.SuggestNext, Value: "x", By: "summarizer"},
	} {
		_, err := st.UpsertWorkSuggestion(sg)
		assert.NoErr(t, err)
	}
	_, _, err = st.AddWorkMergeSuggestion(w2.ID, w3.ID, "同一件事", "steward")
	assert.NoErr(t, err)

	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	wc, ok := find(cards, "work:"+w1.ID)
	assert.True(t, ok)
	assert.Eq(t, "等我", wc.Tag)
	assert.Eq(t, Blocks{Score: 2, Items: 1, Text: "claude 会话在线等你答复"}, wc.Blocks)
	assert.Eq(t, "s-w", wc.Refs.SessionID)
	assert.Len(t, wc.Suggestions, 1)
	assert.Eq(t, "建议目标：支持 iframe", wc.Suggestions[0].Text)
	_, ok = find(cards, "suggestion:"+w1.ID+":goal")
	assert.False(t, ok)

	sc, ok := find(cards, "suggestion:"+w2.ID+":status_hint")
	assert.True(t, ok)
	assert.Eq(t, "建议把状态改为「待验收」", sc.Summary)
	assert.Eq(t, []string{"adopt", "dismiss"}, actionIDs(sc.Actions))
	_, ok = find(cards, "suggestion:"+w3.ID+":next")
	assert.False(t, ok)

	var merge Card
	for _, c := range cards {
		if c.Kind == KindMerge {
			merge = c
		}
	}
	assert.Eq(t, "同一件事", merge.Summary)
	assert.True(t, merge.Refs.MergeID > 0)
	assert.Len(t, cards, 3)
}

func TestTodayDigestStatusAndHandled(t *testing.T) {
	svc, st := newTestService(t)
	putJob(t, st, jobstore.JobRecord{ID: "j1", Agent: "codex", Status: job.StatusDone, StartedAt: ago(50), EndedAt: ago(40), CommitsJSON: `[{"sha":"a"},{"sha":"b"},{"sha":"c"}]`})
	putJob(t, st, jobstore.JobRecord{ID: "j2", Agent: "codex", Status: job.StatusFailed, StartedAt: ago(50), EndedAt: ago(30)})
	putJob(t, st, jobstore.JobRecord{ID: "j3", Agent: "codex", Status: job.StatusDone, StartedAt: ago(500), EndedAt: ago(400)})
	putJob(t, st, jobstore.JobRecord{ID: "j4", Agent: "codex", Status: job.StatusRunning, StartedAt: ago(5)})

	resp, err := svc.Today(Query{Since: ago(60)})
	assert.NoErr(t, err)
	assert.Eq(t, SinceLast{Since: ago(60), JobsDone: 1, JobsFailed: 1, Commits: 3}, resp.Digest.SinceLast)
	assert.StrContains(t, resp.Digest.Title, "工作摘要")
	assert.Eq(t, 0, resp.Snoozed)
	assert.Eq(t, testNow.Unix(), resp.GeneratedAt)
	assert.Eq(t, "v0.127.0", resp.Status.Version)
	assert.Eq(t, 1, resp.Status.Runners.RunningJobs)
	assert.Eq(t, []string{"runner w-mac 离线"}, resp.Status.Alerts)
	assert.True(t, resp.Status.UsageToday.Jobs >= 3)
	assert.NotNil(t, resp.Decisions)

	// since=0 falls back to local midnight.
	resp, err = svc.Today(Query{})
	assert.NoErr(t, err)
	assert.Eq(t, startOfDay(testNow).Unix(), resp.Digest.SinceLast.Since)

	_, err = svc.RecordAction(ActionInput{CardKey: "review:j1"}, "me")
	assert.ErrIs(t, err, ErrInvalidAction)
	h, err := svc.RecordAction(ActionInput{CardKey: "review:j1", ActionID: "accept", Title: "导出", Label: "通过", AdviceActionID: "accept"}, "me")
	assert.NoErr(t, err)
	assert.Eq(t, "review", h.Kind)
	rows, err := svc.HandledSince(7)
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
	assert.Eq(t, Handled{At: testNow.Unix(), Actor: "me", CardKey: "review:j1", Kind: "review", Title: "导出", ActionID: "accept", Label: "通过", AdviceActionID: "accept"}, rows[0])
}

func TestParseHelpers(t *testing.T) {
	adds, dels := parseDiffStat(" 1 file changed, 1 insertion(+)")
	assert.Eq(t, 1, adds)
	assert.Eq(t, 0, dels)
	assert.Eq(t, "a b", firstParagraph("# a\nb\n\nc", 10))
	assert.Eq(t, "ab…", capRunes("abc", 2))
	assert.Eq(t, 2, countSuccessors([]jobstore.PlanTodo{
		{TodoID: "a"}, {TodoID: "b", After: []string{"a"}}, {TodoID: "c", After: []string{"b", "a"}},
	}, "a"))
}
