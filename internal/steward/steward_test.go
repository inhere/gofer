package steward

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/work"
)

type fakeJob struct {
	info JobInfo
	say  []string
}

// fakeHost is a SessionHost whose turns finish instantly (hold keeps them running).
type fakeHost struct {
	mu       sync.Mutex
	jobs     map[string]*fakeJob
	started  []StartSpec
	ended    []string
	seq      int
	hold     bool
	badAgent map[string]string
}

func newFakeHost() *fakeHost {
	return &fakeHost{jobs: map[string]*fakeJob{}, badAgent: map[string]string{}}
}

func (h *fakeHost) StartSession(sp StartSpec) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	id := fmt.Sprintf("job-%d", h.seq)
	h.started = append(h.started, sp)
	h.jobs[id] = &fakeJob{info: JobInfo{ID: id, Agent: sp.Agent, Status: "awaiting_input", Live: true, AwaitingInput: !h.hold, TurnNo: 1, StartedAt: time.Now().Unix()}}
	if h.hold {
		h.jobs[id].info.Status = "running"
	}
	return id, nil
}

func (h *fakeHost) Say(id, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[id]
	if j == nil || !j.info.Live {
		return errors.New("no live session")
	}
	if !j.info.AwaitingInput {
		return errors.New("not awaiting input")
	}
	j.say = append(j.say, text)
	j.info.TurnNo++
	return nil
}

func (h *fakeHost) End(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if j := h.jobs[id]; j != nil {
		j.info.Live, j.info.AwaitingInput, j.info.Status = false, false, "done"
	}
	h.ended = append(h.ended, id)
	return nil
}

func (h *fakeHost) Job(id string) (JobInfo, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if j := h.jobs[id]; j != nil {
		return j.info, true
	}
	return JobInfo{}, false
}

func (h *fakeHost) CheckAgent(a string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if why := h.badAgent[a]; why != "" {
		return errors.New(why)
	}
	return nil
}

func (h *fakeHost) idle(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jobs[id].info.AwaitingInput, h.jobs[id].info.Status = true, "awaiting_input"
}

func (h *fakeHost) said(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.jobs[id].say...)
}

type env struct {
	svc  *Service
	st   *jobstore.Store
	work *work.Service
	host *fakeHost
	cfg  *config.StewardConfig
	wcfg *config.WorkConfig
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "s.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ws := work.New(st)
	h := newFakeHost()
	e := &env{st: st, work: ws, host: h, cfg: &config.StewardConfig{Enabled: true, Agent: "claude-acp"}, wcfg: &config.WorkConfig{}}
	e.svc = New(st, ws, h)
	e.svc.SetConfigFn(func() (config.StewardConfig, config.WorkConfig) { return *e.cfg, *e.wcfg })
	e.svc.askWait, e.svc.pollEvery, e.svc.reviewTimeout = 200*time.Millisecond, 5*time.Millisecond, 2*time.Second
	// LIFO: both run before the store closes (gofer-r7am).
	t.Cleanup(func() { assert.NoErr(t, ws.Close()) })
	t.Cleanup(func() { assert.NoErr(t, e.svc.Close()) })
	return e
}

func (e *env) item(t *testing.T, title string) jobstore.WorkItem {
	t.Helper()
	w, err := e.st.CreateWorkItem(jobstore.WorkItemInput{Title: title, By: "human:a"})
	assert.NoErr(t, err)
	return w
}

func TestStewardIsOffByDefaultAndNeedsAnAgent(t *testing.T) {
	e := newEnv(t)
	e.cfg.Enabled = false
	_, err := e.svc.Ask(context.Background(), "还有什么没完成", "human:a")
	assert.True(t, errors.Is(err, ErrDisabled))
	_, err = e.svc.Start(context.Background())
	assert.True(t, errors.Is(err, ErrDisabled))

	e.cfg.Enabled, e.cfg.Agent = true, ""
	_, err = e.svc.Start(context.Background())
	assert.True(t, errors.Is(err, ErrNoAgent))

	e.cfg.Agent = "claude-acp"
	e.host.badAgent["claude-acp"] = "本机没有找到该命令"
	_, err = e.svc.Start(context.Background())
	assert.True(t, err != nil && strings.Contains(err.Error(), "本机没有找到该命令"))
	assert.Eq(t, 0, len(e.host.started))
	assert.Eq(t, "本机没有找到该命令", e.svc.Status().AgentError)
}

func TestStartIsIdempotentAndStopForgetsTheSession(t *testing.T) {
	e := newEnv(t)
	e.item(t, "修登录")
	res, err := e.svc.Start(context.Background())
	assert.NoErr(t, err)
	assert.True(t, res.Started)
	again, err := e.svc.Start(context.Background())
	assert.NoErr(t, err)
	assert.False(t, again.Started)
	assert.Eq(t, res.JobID, again.JobID)
	assert.Eq(t, 1, len(e.host.started))

	sp := e.host.started[0]
	assert.Eq(t, "claude-acp", sp.Agent)
	assert.Eq(t, config.DefaultProjectKey, sp.Project)
	assert.Eq(t, 30*60, sp.IdleSec)
	assert.True(t, strings.Contains(sp.Prompt, "# 你是工作管家"))
	assert.True(t, strings.Contains(sp.Prompt, "修登录"))
	assert.True(t, strings.HasSuffix(sp.Prompt, readyLine))

	st := e.svc.Status()
	assert.Eq(t, StateIdle, st.State)
	assert.Eq(t, res.JobID, st.JobID)

	was, err := e.svc.Stop()
	assert.NoErr(t, err)
	assert.True(t, was)
	assert.Eq(t, StateStopped, e.svc.Status().State)
	was, _ = e.svc.Stop()
	assert.False(t, was)
}

func TestIdleEndedSessionReadsAsNotStartedAndIsRebuilt(t *testing.T) {
	e := newEnv(t)
	first, _ := e.svc.Start(context.Background())
	// The job service ended it by itself (idle timeout).
	assert.NoErr(t, e.host.End(first.JobID))
	assert.Eq(t, StateStopped, e.svc.Status().State)
	ask, err := e.svc.Ask(context.Background(), "我手上还有什么没完成？", "human:a")
	assert.NoErr(t, err)
	assert.True(t, ask.Started)
	assert.True(t, ask.JobID != first.JobID)
	assert.Eq(t, 2, len(e.host.started))
}

func TestAskStartsWithTheQuestionThenSpeaksToTheLiveSession(t *testing.T) {
	e := newEnv(t)
	e.item(t, "设备到货")
	first, err := e.svc.Ask(context.Background(), "我手上还有什么没完成？", "human:me")
	assert.NoErr(t, err)
	assert.True(t, first.Started)
	prompt := e.host.started[0].Prompt
	assert.True(t, strings.Contains(prompt, "我手上还有什么没完成？") && strings.Contains(prompt, "human:me"))
	assert.True(t, strings.Contains(prompt, "设备到货")) // the prime rides in front of the question

	second, err := e.svc.Ask(context.Background(), "今天去现场要做什么？", "human:me")
	assert.NoErr(t, err)
	assert.False(t, second.Started)
	assert.Eq(t, first.JobID, second.JobID)
	said := e.host.said(first.JobID)
	assert.Eq(t, 1, len(said))
	assert.True(t, strings.Contains(said[0], "今天去现场要做什么？"))

	_, err = e.svc.Ask(context.Background(), "  ", "human:me")
	assert.True(t, errors.Is(err, ErrEmpty))
}

func TestAskWhileTheSessionIsMidTurnTimesOutAsBusy(t *testing.T) {
	e := newEnv(t)
	e.host.hold = true
	res, err := e.svc.Start(context.Background())
	assert.NoErr(t, err)
	assert.Eq(t, StateRunning, e.svc.Status().State)
	_, err = e.svc.Ask(context.Background(), "在吗", "human:a")
	assert.True(t, errors.Is(err, ErrBusy))

	// Once it is between turns again the ask goes through.
	e.host.idle(res.JobID)
	_, err = e.svc.Ask(context.Background(), "在吗", "human:a")
	assert.NoErr(t, err)
}

// Switching the agent ends the old session; the next need rebuilds on the new agent from
// the prime, which still carries the notes and the in-flight request ledger.
func TestSwitchingAgentRebuildsFromThePrimeAndRecallsRequestsAndNotes(t *testing.T) {
	e := newEnv(t)
	w := e.item(t, "zy-bsly 现场换设备")
	sess, _ := e.st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-recall1", Agent: "claude", ProjectKey: "p", Cwd: "/ws"})
	assert.NoErr(t, e.st.AttachWorkSession(w.ID, sess.SessionID, "human:a"))
	req, err := e.st.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: w.ID, SessionID: sess.SessionID,
		Kind: jobstore.WorkRequestHandoff, By: "steward(claude-acp)"})
	assert.NoErr(t, err)
	_, _, err = e.st.MarkWorkRequest(req.ID, jobstore.WorkRequestSent, "turn", "", jobstore.WorkRequestPending)
	assert.NoErr(t, err)
	_, err = e.svc.SetNotes("- zy-bsly 现场一般周三去\n", "human:a", 0)
	assert.NoErr(t, err)

	a, err := e.svc.Start(context.Background())
	assert.NoErr(t, err)
	assert.True(t, strings.Contains(e.host.started[0].Prompt, "zy-bsly 现场一般周三去"))

	// Switch to codex: reconcile ends the old session, the next ask opens a new one.
	e.cfg.Agent = "codex-acp"
	e.svc.Reconcile()
	assert.Eq(t, []string{a.JobID}, e.host.ended)
	assert.Eq(t, StateStopped, e.svc.Status().State)

	b, err := e.svc.Ask(context.Background(), "在途的请求有哪些？", "human:a")
	assert.NoErr(t, err)
	assert.True(t, b.Started)
	assert.True(t, b.JobID != a.JobID)
	sp := e.host.started[1]
	assert.Eq(t, "codex-acp", sp.Agent)
	for _, want := range []string{"zy-bsly 现场一般周三去", req.ID, "交接请求", "已发送待回复", w.ID, "在途：交接请求"} {
		assert.True(t, strings.Contains(sp.Prompt, want), want)
	}
}

func TestAskAfterAgentSwitchWithoutReconcileStillUsesTheNewAgent(t *testing.T) {
	e := newEnv(t)
	a, _ := e.svc.Start(context.Background())
	e.cfg.Agent = "omp-acp"
	res, err := e.svc.Ask(context.Background(), "hi", "human:a")
	assert.NoErr(t, err)
	assert.True(t, res.Started)
	assert.True(t, res.JobID != a.JobID)
	assert.Eq(t, "omp-acp", e.host.started[1].Agent)
	assert.Eq(t, []string{a.JobID}, e.host.ended)
}

func TestReconcileEndsASessionWhenTheStewardIsSwitchedOff(t *testing.T) {
	e := newEnv(t)
	a, _ := e.svc.Start(context.Background())
	e.cfg.Enabled = false
	e.svc.Reconcile()
	assert.Eq(t, []string{a.JobID}, e.host.ended)
	assert.Eq(t, StateStopped, e.svc.Status().State)
}

func TestRestartEndsAndRebuilds(t *testing.T) {
	e := newEnv(t)
	a, _ := e.svc.Start(context.Background())
	b, err := e.svc.Restart(context.Background())
	assert.NoErr(t, err)
	assert.True(t, b.Started)
	assert.True(t, b.JobID != a.JobID)
	assert.Eq(t, []string{a.JobID}, e.host.ended)
}

func TestCloseStopsRunAndReviewWatchers(t *testing.T) {
	e := newEnv(t)
	e.cfg.Enabled = false // Run only loops
	runDone := make(chan struct{})
	go func() { e.svc.Run(make(chan struct{})); close(runDone) }() // stop never closes

	started, finished := make(chan struct{}), make(chan struct{})
	assert.True(t, e.svc.spawn(func() {
		close(started)
		e.svc.life.Lock()
		ch := e.svc.closingCh()
		e.svc.life.Unlock()
		<-ch
		close(finished)
	}))
	<-started
	assert.NoErr(t, e.svc.Close())
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the background task ended")
	}
	select {
	case <-runDone:
	case <-time.After(wait.Timeout(t, 2*time.Second)):
		t.Fatal("Run did not return after Close")
	}
	assert.False(t, e.svc.spawn(func() { t.Error("spawn ran after Close") }))
	assert.NoErr(t, e.svc.Close())
}

func TestCloseIsBounded(t *testing.T) {
	e := newEnv(t)
	old := closeWait
	closeWait = 50 * time.Millisecond
	defer func() { closeWait = old }()
	release := make(chan struct{})
	assert.True(t, e.svc.spawn(func() { <-release }))
	assert.Err(t, e.svc.Close())
	close(release)
	e.svc.WaitIdle()
}
