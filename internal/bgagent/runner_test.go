package bgagent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
)

// testClock is a settable clock shared by the runner and the store.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var clockT0 = time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)

type fixture struct {
	t     *testing.T
	store *jobstore.Store
	path  string
	clk   *testClock
	r     *Runner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bg.db")
	f := &fixture{t: t, path: path, clk: &testClock{t: clockT0}}
	f.open()
	return f
}

func (f *fixture) open() {
	st, err := jobstore.Open(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	st.SetClock(f.clk.Now)
	f.t.Cleanup(func() { _ = st.Close() })
	f.store = st
	f.r = New(st, Options{Now: f.clk.Now})
}

// reopen simulates a server restart: the database is closed and opened again.
func (f *fixture) reopen() {
	f.t.Helper()
	if err := f.store.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.open()
}

// fakeExec is a scripted Executor: it records every request and answers from usages /
// errs by call number.
type fakeExec struct {
	caps   Caps
	calls  []ExecRequest
	usages []*runner.Usage
	errs   map[int]error // call number (1-based) -> error
	block  bool          // block until the context ends
	sid    func(n int, r ExecRequest) string
}

func (e *fakeExec) Caps(string) Caps { return e.caps }
func (e *fakeExec) Execute(ctx context.Context, r ExecRequest) (ExecResult, error) {
	e.calls = append(e.calls, r)
	n := len(e.calls)
	if e.block {
		<-ctx.Done()
		return ExecResult{JobID: fmt.Sprintf("job-%d", n)}, ctx.Err()
	}
	if err := e.errs[n]; err != nil {
		return ExecResult{JobID: fmt.Sprintf("job-%d", n)}, err
	}
	res := ExecResult{JobID: fmt.Sprintf("job-%d", n), Output: []byte("answer")}
	res.SessionID = fmt.Sprintf("sess-%d", n)
	if r.Mode != ModeFresh {
		res.SessionID = r.SessionID // the same agent session continues
	}
	if e.sid != nil {
		res.SessionID = e.sid(n, r)
	}
	if n-1 < len(e.usages) {
		res.Usage = e.usages[n-1]
	}
	return res, nil
}

func ratioUsage(ratio float64) *runner.Usage {
	return &runner.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CostUSD: 0.01,
		Context: &runner.ContextUsage{Ratio: ratio, Source: runner.ContextSourceTurnEnd}}
}

func baseReq() Request {
	return Request{Key: Key{Kind: KindJobCheck, Agent: "claude", Project: "p1", WorkDir: "/w", Model: "haiku"},
		Prompt: "check this", Policy: Policy{MaxRuns: 3, MaxContextRatio: 0.7}}
}

func (f *fixture) run(e *fakeExec, req Request) (Result, error) {
	f.t.Helper()
	f.clk.Advance(time.Second)
	return f.r.Run(context.Background(), e, req)
}

func (f *fixture) active(k Key) jobstore.BackgroundSession {
	f.t.Helper()
	b, ok, err := f.store.ActiveBackgroundSession(k.Kind, k.Project, k.WorkDir, k.Agent, k.Model)
	if err != nil || !ok {
		f.t.Fatalf("no active session for %+v: ok=%v err=%v", k, ok, err)
	}
	return b
}

func (f *fixture) audits(kind string) []string {
	f.t.Helper()
	evs, err := f.store.ListAuditEventsSince(kind, 0, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Detail)
	}
	return out
}

func TestRunFreshThenResume(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}, usages: []*runner.Usage{ratioUsage(0.2), ratioUsage(0.3)}}
	r1, err := f.run(e, baseReq())
	if err != nil || r1.Mode != ModeFresh || r1.Reason != ReasonNone {
		t.Fatalf("first run = %+v %v", r1, err)
	}
	r2, err := f.run(e, baseReq())
	if err != nil || r2.Mode != ModeResume {
		t.Fatalf("second run = %+v %v", r2, err)
	}
	if got := e.calls[1]; got.SessionID != "sess-1" || got.LastJobID != "job-1" || got.Mode != ModeResume {
		t.Fatalf("resume request = %+v", got)
	}
	b := f.active(baseReq().Key)
	if b.Runs != 2 || b.LastJobID != "job-2" || b.SessionID != "sess-1" || b.ContextRatio() < 0.29 || b.ContextRatio() > 0.31 {
		t.Fatalf("book = %+v ratio=%v", b, b.ContextRatio())
	}
	st, _ := f.r.Status(KindJobCheck, baseReq().Policy)
	if len(st.Sessions) != 1 || st.Sessions[0].ContextRatio == nil || st.Today.Runs != 2 || st.Today.UsageRuns != 2 {
		t.Fatalf("status = %+v", st)
	}
}

func TestRunFreshWhenAgentCannotResume(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{}}
	for i := 0; i < 2; i++ {
		if res, err := f.run(e, baseReq()); err != nil || res.Mode != ModeFresh {
			t.Fatalf("run %d = %+v %v", i, res, err)
		}
	}
	if e.calls[1].Mode != ModeFresh || e.calls[1].SessionID != "" {
		t.Fatalf("second request = %+v", e.calls[1])
	}
	if b := f.active(baseReq().Key); b.Runs != 1 {
		t.Fatalf("a non-resumable agent starts a new record each run: %+v", b)
	}
}

func TestRotationByContextRatio(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}, usages: []*runner.Usage{ratioUsage(0.75)}}
	_, _ = f.run(e, baseReq())
	r2, err := f.run(e, baseReq())
	if err != nil || r2.Mode != ModeFresh || r2.Reason != ReasonContextRatio {
		t.Fatalf("a 0.75 session must rotate: %+v %v", r2, err)
	}
	if b := f.active(baseReq().Key); b.Runs != 1 || b.RotationReason != ReasonContextRatio || b.SessionID == "sess-1" {
		t.Fatalf("rotated book = %+v", b)
	}
	got := f.audits(jobstore.AuditBackgroundRotated)
	if len(got) != 1 || !strings.Contains(got[0], `"reason":"context_ratio"`) || !strings.Contains(got[0], `"from_session":"sess-1"`) {
		t.Fatalf("rotation audit = %v", got)
	}
}

func TestRotationByRunCountWhenNoUsage(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}} // never reports usage
	modes := []Mode{}
	for i := 0; i < 5; i++ {
		res, err := f.run(e, baseReq())
		if err != nil {
			t.Fatal(err)
		}
		modes = append(modes, res.Mode)
	}
	// MaxRuns 3: fresh, resume, resume, then the 3-run session rotates, resume
	want := []Mode{ModeFresh, ModeResume, ModeResume, ModeFresh, ModeResume}
	for i := range want {
		if modes[i] != want[i] {
			t.Fatalf("modes = %v, want %v", modes, want)
		}
	}
	if got := f.audits(jobstore.AuditBackgroundRotated); len(got) != 1 || !strings.Contains(got[0], `"reason":"max_runs"`) {
		t.Fatalf("rotation audit = %v", got)
	}
}

func TestRotationByExplicitMaxAgeOnly(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}}
	req := baseReq()
	req.Policy = Policy{MaxRuns: 100}
	_, _ = f.run(e, req)
	f.clk.Advance(1000 * time.Hour)
	if res, _ := f.run(e, req); res.Mode != ModeResume {
		t.Fatalf("without max_age a session never ages out: %+v", res)
	}
	req.Policy.MaxAge = time.Hour
	if res, _ := f.run(e, req); res.Mode != ModeFresh || res.Reason != ReasonMaxAge {
		t.Fatalf("explicit max_age must rotate: %+v", res)
	}
}

func TestClearContextKeepsSessionID(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true, CanClearContext: true}, usages: []*runner.Usage{ratioUsage(0.9)}}
	_, _ = f.run(e, baseReq())
	r2, err := f.run(e, baseReq())
	if err != nil || r2.Mode != ModeClearContext || r2.Reason != ReasonContextRatio {
		t.Fatalf("second run = %+v %v", r2, err)
	}
	if e.calls[1].SessionID != "sess-1" {
		t.Fatalf("clear request = %+v", e.calls[1])
	}
	b := f.active(baseReq().Key)
	if b.SessionID != "sess-1" || b.Runs != 1 || b.RotationReason != ReasonContextRatio {
		t.Fatalf("a cleared session keeps its id and restarts the counters: %+v", b)
	}
}

func TestClearContextUnsupportedFallsBackToFresh(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true, CanClearContext: true}, usages: []*runner.Usage{ratioUsage(0.9)},
		errs: map[int]error{2: fmt.Errorf("agent says no: %w", ErrClearUnsupported)}}
	_, _ = f.run(e, baseReq())
	r2, err := f.run(e, baseReq())
	if err != nil || r2.Mode != ModeFresh || r2.Reason != ReasonClearUnsupported {
		t.Fatalf("fallback = %+v %v", r2, err)
	}
	if len(e.calls) != 3 || e.calls[2].Mode != ModeFresh {
		t.Fatalf("calls = %+v", e.calls)
	}
	if b := f.active(baseReq().Key); b.SessionID == "sess-1" || b.Runs != 1 {
		t.Fatalf("book = %+v", b)
	}
}

func TestSessionLostFallsBackToFresh(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}, errs: map[int]error{2: fmt.Errorf("gone: %w", ErrSessionLost)}}
	_, _ = f.run(e, baseReq())
	r2, err := f.run(e, baseReq())
	if err != nil || r2.Mode != ModeFresh || r2.Reason != ReasonSessionLost {
		t.Fatalf("fallback = %+v %v", r2, err)
	}
	b := f.active(baseReq().Key)
	if b.Runs != 1 || b.SessionID == "sess-1" {
		t.Fatalf("book = %+v", b)
	}
	st, _ := f.r.Status(KindJobCheck, baseReq().Policy)
	if st.Today.Runs != 3 {
		t.Fatalf("every attempt is a logged run: %+v", st.Today)
	}
}

func TestKeyChangeAndKindIsolation(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}}
	_, _ = f.run(e, baseReq())
	// another project / work_dir / kind is another scope: it starts fresh and leaves the
	// original session alone
	for name, mut := range map[string]func(*Request){
		"project":  func(r *Request) { r.Project = "p2" },
		"work_dir": func(r *Request) { r.WorkDir = "/other" },
		"kind":     func(r *Request) { r.Kind = KindSummarizer },
	} {
		req := baseReq()
		mut(&req)
		res, err := f.run(e, req)
		if err != nil || res.Mode != ModeFresh {
			t.Fatalf("%s change must start fresh: %+v %v", name, res, err)
		}
	}
	if res, _ := f.run(e, baseReq()); res.Mode != ModeResume {
		t.Fatalf("the original session must still be continued: %+v", res)
	}
	// another agent or model in the same scope never continues the session: it starts
	// fresh, says why, and replaces the old session (one live session per scope)
	for name, mut := range map[string]func(*Request){
		"agent": func(r *Request) { r.Agent = "codex" },
		"model": func(r *Request) { r.Model = "sonnet" },
	} {
		f := newFixture(t)
		_, _ = f.run(e, baseReq())
		req := baseReq()
		mut(&req)
		res, err := f.run(e, req)
		if err != nil || res.Mode != ModeFresh || res.Reason != ReasonKeyChanged {
			t.Fatalf("%s change = %+v %v, want fresh / key_changed", name, res, err)
		}
		if res, _ := f.run(e, req); res.Mode != ModeResume {
			t.Fatalf("%s: the new session is continued next: %+v", name, res)
		}
		if st, _ := f.r.Status(KindJobCheck, req.Policy); len(st.Sessions) != 1 {
			t.Fatalf("%s: one live session per scope, got %d", name, len(st.Sessions))
		}
	}
}

func TestManualResetKeepsHistory(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}, usages: []*runner.Usage{ratioUsage(0.1), ratioUsage(0.1)}}
	_, _ = f.run(e, baseReq())
	n, err := f.r.Reset(KindJobCheck, "p1", "/w", "person:alice")
	if err != nil || n != 1 {
		t.Fatalf("reset = %d %v", n, err)
	}
	st, _ := f.r.Status(KindJobCheck, baseReq().Policy)
	if len(st.Sessions) != 0 || st.Today.Runs != 1 {
		t.Fatalf("a reset ends the session but keeps the usage history: %+v", st)
	}
	res, err := f.run(e, baseReq())
	if err != nil || res.Mode != ModeFresh || res.Reason != ReasonManualReset {
		t.Fatalf("after reset = %+v %v", res, err)
	}
	// the run after that continues the NEW session
	if res, _ := f.run(e, baseReq()); res.Mode != ModeResume {
		t.Fatalf("third run = %+v", res)
	}
	if got := f.audits("background_agent.reset"); len(got) != 1 || !strings.Contains(got[0], "person:alice") {
		t.Fatalf("reset audit = %v", got)
	}
}

func TestHardStopDailyLimit(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}}
	req := baseReq()
	req.Limits.DailyRuns = 2
	_, _ = f.run(e, req)
	_, _ = f.run(e, req)
	_, err := f.run(e, req)
	var stop *HardStopError
	if !errors.As(err, &stop) || stop.Reason != StopDailyLimit || stop.Limit != 2 || stop.Used != 2 {
		t.Fatalf("err = %v, want daily_limit hard stop", err)
	}
	if len(e.calls) != 2 {
		t.Fatalf("a hard stop must not run the agent: %d calls", len(e.calls))
	}
	if got := f.audits(jobstore.AuditBackgroundStopped); len(got) != 1 || !strings.Contains(got[0], `"reason":"daily_limit"`) {
		t.Fatalf("stop audit = %v", got)
	}
	st, _ := f.r.Status(KindJobCheck, req.Policy)
	if st.Today.Runs != 2 || st.Today.Blocked != 1 {
		t.Fatalf("a blocked attempt is logged but is not a run: %+v", st.Today)
	}
	// the next UTC day the counter starts over
	f.clk.Advance(24 * time.Hour)
	if _, err := f.run(e, req); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

func TestHardStopBudgets(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}} // each run costs 0.01 USD / 110 tokens
	e.usages = []*runner.Usage{ratioUsage(0.1), ratioUsage(0.1), ratioUsage(0.1)}
	req := baseReq()
	req.Limits.DailyCostUSD = 0.02
	_, _ = f.run(e, req)
	_, _ = f.run(e, req)
	_, err := f.run(e, req)
	var stop *HardStopError
	if !errors.As(err, &stop) || stop.Reason != StopDailyCost {
		t.Fatalf("err = %v, want a cost budget hard stop", err)
	}
	// the token budget is its own stop, the cost one is off
	f2 := newFixture(t)
	e2 := &fakeExec{caps: Caps{CanResume: true}, usages: []*runner.Usage{ratioUsage(0.1)}}
	req2 := baseReq()
	req2.Limits.DailyTokens = 100
	_, _ = f2.run(e2, req2)
	if _, err := f2.run(e2, req2); !errors.As(err, &stop) || stop.Reason != StopDailyTokens {
		t.Fatalf("err = %v, want a token budget hard stop", err)
	}
}

func TestBudgetCountsOnlyReportedUsage(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}} // never reports usage
	req := baseReq()
	req.Limits.DailyCostUSD, req.Limits.DailyTokens = 0.0001, 1
	for i := 0; i < 4; i++ {
		if _, err := f.run(e, req); err != nil {
			t.Fatalf("run %d: unknown usage must not trip a budget: %v", i, err)
		}
	}
}

func TestHardStopTimeout(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}, block: true}
	req := baseReq()
	req.Limits.Timeout = 30 * time.Millisecond
	_, err := f.run(e, req)
	var stop *HardStopError
	if !errors.As(err, &stop) || stop.Reason != StopTimeout {
		t.Fatalf("err = %v, want a timeout hard stop", err)
	}
	if got := f.audits(jobstore.AuditBackgroundStopped); len(got) != 1 || !strings.Contains(got[0], `"reason":"timeout"`) {
		t.Fatalf("stop audit = %v", got)
	}
	st, _ := f.r.Status(KindJobCheck, req.Policy)
	if st.Today.LastStatus != jobstore.BackgroundRunTimedOut || len(st.Sessions) != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestCancelKeepsTheSession(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}}
	_, _ = f.run(e, baseReq())
	blocker := &fakeExec{caps: Caps{CanResume: true}, block: true}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	f.clk.Advance(time.Second)
	_, err := f.r.Run(ctx, blocker, baseReq())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	var stop *HardStopError
	if errors.As(err, &stop) {
		t.Fatalf("a cancel is not a hard stop: %v", err)
	}
	if b := f.active(baseReq().Key); b.SessionID != "sess-1" || b.Runs != 1 {
		t.Fatalf("a cancelled run must leave the book alone: %+v", b)
	}
	st, _ := f.r.Status(KindJobCheck, baseReq().Policy)
	if st.Today.LastStatus != jobstore.BackgroundRunCanceled {
		t.Fatalf("status = %+v", st.Today)
	}
}

func TestOtherFailureDoesNotDropTheSession(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}, errs: map[int]error{2: errors.New("provider overloaded")}}
	_, _ = f.run(e, baseReq())
	if _, err := f.run(e, baseReq()); err == nil {
		t.Fatal("a failing turn must report its error")
	}
	if b := f.active(baseReq().Key); b.SessionID != "sess-1" || b.Runs != 1 {
		t.Fatalf("book = %+v", b)
	}
}

func TestRestartRecoversTheBook(t *testing.T) {
	f := newFixture(t)
	e := &fakeExec{caps: Caps{CanResume: true}, usages: []*runner.Usage{ratioUsage(0.4)}}
	_, _ = f.run(e, baseReq())
	f.reopen()
	res, err := f.run(e, baseReq())
	if err != nil || res.Mode != ModeResume {
		t.Fatalf("after a restart = %+v %v", res, err)
	}
	if got := e.calls[1]; got.SessionID != "sess-1" || got.LastJobID != "job-1" {
		t.Fatalf("resume request = %+v", got)
	}
	// the context level survived too: a restart must not forget a nearly full session
	f2 := newFixture(t)
	e2 := &fakeExec{caps: Caps{CanResume: true}, usages: []*runner.Usage{ratioUsage(0.9)}}
	_, _ = f2.run(e2, baseReq())
	f2.reopen()
	if res, _ := f2.run(e2, baseReq()); res.Mode != ModeFresh || res.Reason != ReasonContextRatio {
		t.Fatalf("a restart forgot the context level: %+v", res)
	}
}

func TestObserveFoldsInLateUsage(t *testing.T) {
	f := newFixture(t)
	key := Key{Kind: KindSteward, Agent: "acp", Project: "p1"}
	plan := f.r.Plan(key, Policy{MaxContextRatio: 0.7}, Caps{CanResume: true})
	if err := f.r.Commit(plan, Outcome{JobID: "job-s1", Mode: ModeFresh}); err != nil {
		t.Fatal(err)
	}
	if b := f.active(key); !strings.HasPrefix(b.SessionID, jobstore.BackgroundLocalSessionPrefix) {
		t.Fatalf("a run with no agent-side id gets a made-up one: %+v", b)
	}
	f.r.Observe(key, "job-s1", "agent-sess", &runner.Usage{TotalTokens: 500, CostUSD: 0.5,
		Context: &runner.ContextUsage{UsedTokens: 180000, LimitTokens: 200000}})
	b := f.active(key)
	if b.SessionID != "agent-sess" || b.ContextLimit != 200000 || b.ContextUsed != 180000 {
		t.Fatalf("observed book = %+v", b)
	}
	st, _ := f.r.Status(KindSteward, Policy{})
	if st.Today.CostUSD != 0.5 || st.Today.TotalTokens != 500 {
		t.Fatalf("late usage must reach today's totals: %+v", st.Today)
	}
	// and the next plan rotates on it
	if p := f.r.Plan(key, Policy{MaxContextRatio: 0.7}, Caps{CanResume: true}); p.Decision.Mode != ModeFresh || p.Decision.Reason != ReasonContextRatio {
		t.Fatalf("plan = %+v", p.Decision)
	}
	// a job that is no longer the session's newest does not touch it
	f.r.Observe(key, "job-other", "zzz", &runner.Usage{Context: &runner.ContextUsage{Ratio: 0.1}})
	if f.active(key).SessionID != "agent-sess" {
		t.Fatal("an old job's usage rewrote the session")
	}
}
