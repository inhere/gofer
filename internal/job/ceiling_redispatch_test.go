package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// The tests here pin the SECOND half of a caller ceiling (design 2026-10-18 §准入): a
// re-submission gofer starts on a job's behalf must TERMINATE when admission refuses it
// (ErrCeilingDenied) instead of arranging another attempt, and say so on the source job.
// The retry sweep, the automatic continuation, the failover, the wakeup and a plan item
// each have a case; a ceiling the caller's own submit still passes — but the DERIVED
// request does not — is what makes them discriminating rather than a repeat of the
// admission tests in ceiling_test.go.

// withCeilingOnCfg returns cfg with ONE caller carrying a ceiling (id). mutate sets the
// dimensions; nil leaves the strict defaults (no project, no machine runner, no exec).
func withCeilingOnCfg(cfg *config.Config, id string, mutate func(*config.CallerCeiling)) *config.Config {
	ceil := &config.CallerCeiling{}
	if mutate != nil {
		mutate(ceil)
	}
	cfg.Server.Callers = []config.CallerConfig{{ID: id, Ceiling: ceil}}
	return cfg
}

// reloadCeiling tightens the ceiling of a RUNNING service: the operator restricts a
// caller after the work it caused is already in flight, which is the only way a derived
// submission can be refused while its source was admitted.
func reloadCeiling(t *testing.T, s *Service, id string, mutate func(*config.CallerCeiling)) {
	t.Helper()
	s.Reload(withCeilingOnCfg(s.Config(), id, mutate))
}

// waitForJobEvents waits until a job's timeline carries every named event and returns the
// types in order. The takeover paths run AFTER the status flips (see finish), so polling
// the job's status is not enough to know the refusal has been recorded.
func waitForJobEvents(t *testing.T, s *Service, jobID string, want ...string) []string {
	t.Helper()
	var got []string
	wait.For(t, 15*time.Second, "job "+jobID+" events "+strings.Join(want, ","), func() (bool, any) {
		got = eventTypes(t, s, jobID)
		for _, w := range want {
			if !contains(got, w) {
				return false, got
			}
		}
		return true, got
	})
	return got
}

// ceilingDeniedDetail returns the detail_json of a job's first event of a type, so a test
// can assert the REASON a refusal recorded rather than only that something was recorded.
func ceilingDeniedDetail(t *testing.T, s *Service, jobID, eventType string) string {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", jobID, err)
	}
	for _, ev := range evs {
		if ev.Type == eventType {
			return ev.Detail
		}
	}
	t.Fatalf("job %s has no %s event: %v", jobID, eventType, evs)
	return ""
}

// TestRetrySweepCancelsRetryOnCeilingDenied: a due retry whose replayed request the
// caller's (now tighter) ceiling refuses is CANCELLED — the row cannot be claimed again
// on every tick — and the source job records job.retry_cancelled with the reason
// (design §准入, mirroring the design's retry contract in retry_sweep.go).
func TestRetrySweepCancelsRetryOnCeilingDenied(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newTestService(t, root)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", CallerID: "ci-bot", OriginCaller: "ci-bot",
		Cmd: []string{"go", "version", "--nope"}, Cwd: ".", TimeoutSec: 30,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed", src.Status)
	}
	rows := retryRows(t, s, src.ID)
	if len(rows) != 1 {
		t.Fatalf("retry rows = %+v, want the one pending row the sweeper needs", rows)
	}
	// The ceiling arrives after the job ran: "self" is no longer in the caller's scope,
	// so nothing this caller derived may be submitted again.
	reloadCeiling(t, s, "ci-bot", nil)

	started, failed, err := s.SweepDueRetries(time.Now().Unix(), 10, 60)
	if err != nil {
		t.Fatalf("SweepDueRetries: %v", err)
	}
	if started != 0 || failed != 1 {
		t.Fatalf("sweep = started %d failed %d, want 0/1", started, failed)
	}
	rec, ok, gerr := s.GetRetry(rows[0].ID)
	if gerr != nil || !ok {
		t.Fatalf("GetRetry(%s): ok=%v err=%v", rows[0].ID, ok, gerr)
	}
	if rec.State != jobstore.RetryCancelled {
		t.Fatalf("retry state = %q, want %q (a denied retry must not be claimable again)", rec.State, jobstore.RetryCancelled)
	}
	waitForJobEvents(t, s, src.ID, EventJobRetryCancelled)
	if detail := ceilingDeniedDetail(t, s, src.ID, EventJobRetryCancelled); !strings.Contains(detail, "caller") {
		t.Fatalf("retry_cancelled detail = %q, want the ceiling refusal", detail)
	}
	// Nothing was submitted: there is no attempt 2.
	jobs, lerr := s.Meta().ListJobs(jobstore.ListQuery{})
	if lerr != nil {
		t.Fatalf("ListJobs: %v", lerr)
	}
	for _, j := range jobs {
		if j.Attempt > 1 {
			t.Fatalf("a cancelled retry produced job %s (attempt %d)", j.ID, j.Attempt)
		}
	}
}

// TestRetrySweepFailsClosedWhenSourceGone: a row whose SOURCE job has been pruned, whose
// stored request predates the ceiling owner, and whose chain still names a RESTRICTED
// caller is refused rather than replayed as an unchecked job (design 2026-10-18: 解析不到
// 上限所有者 → 受限链失败关闭).
func TestRetrySweepFailsClosedWhenSourceGone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newTestService(t, root)
	reloadCeiling(t, s, "ci-bot", func(c *config.CallerCeiling) { c.Projects = []string{"self"} })

	now := time.Now().Unix()
	rec := jobstore.RetryRecord{
		ID: jobstore.NewRetryID(), SourceJobID: "job-pruned", Attempt: 2,
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":".","caller_id":"ci-bot"}`,
		State:       jobstore.RetryPending, NextRunAt: now - 1, CreatedAt: now - 10,
	}
	if err := s.Meta().InsertRetry(rec); err != nil {
		t.Fatalf("InsertRetry: %v", err)
	}
	if _, failed, err := s.SweepDueRetries(now, 10, 60); err != nil {
		t.Fatalf("SweepDueRetries: %v", err)
	} else if failed != 1 {
		t.Fatalf("sweep failed = %d, want 1", failed)
	}
	got, ok, err := s.GetRetry(rec.ID)
	if err != nil || !ok {
		t.Fatalf("GetRetry(%s): ok=%v err=%v", rec.ID, ok, err)
	}
	if got.State != jobstore.RetryCancelled {
		t.Fatalf("retry state = %q, want %q", got.State, jobstore.RetryCancelled)
	}
	// The row is gone, so the refusal is recorded on the source job's scope anyway.
	evs, err := s.ListJobEvents(rec.SourceJobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", rec.SourceJobID, err)
	}
	if len(evs) != 1 || evs[0].Type != EventJobRetryCancelled {
		t.Fatalf("source events = %+v, want one %s", evs, EventJobRetryCancelled)
	}
	if !strings.Contains(evs[0].Detail, "restricted") {
		t.Fatalf("detail = %q, want the fail-closed reason", evs[0].Detail)
	}
}

// TestAutoResumeStopsOnCeilingDenied: the same-agent continuation of a transient failure
// runs the agent's resume argv through the built-in exec carrier, which a caller allowed
// to use the AGENT but not exec may not submit. The chain stops there: no continuation
// job, the source job goes terminal (late) and records job.ceiling_denied with path
// auto-resume.
func TestAutoResumeStopsOnCeilingDenied(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := withCeilingOnCfg(
		autoResumeCfg(testcmd.Path(t), root, "codex", "Selected model is at capacity", nil), "ci-bot",
		func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
			c.Agents = []string{"codex"}
			// No AllowExec: the caller may run the agent, not a command of its choosing —
			// and the continuation carrier IS such a command (the resume argv).
		})
	s := newServiceFromCfg(t, root, cfg)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local", OriginCaller: "ci-bot",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-ceil",
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed (the ceiling admits the agent job)", src.Status)
	}
	types := waitForJobEvents(t, s, src.ID, EventJobCeilingDenied, EventJobTerminal)
	if !hasSubsequence(types, []string{EventJobCeilingDenied, EventJobTerminal}) {
		t.Fatalf("events = %v, want the refusal BEFORE the late terminal", types)
	}
	if got, _ := s.Get(src.ID); got.AutoResumedBy != "" {
		t.Fatalf("auto_resumed_by = %q, want none (the continuation was refused)", got.AutoResumedBy)
	}
	detail := ceilingDeniedDetail(t, s, src.ID, EventJobCeilingDenied)
	if !strings.Contains(detail, `"path":"auto-resume"`) || !strings.Contains(detail, "exec") {
		t.Fatalf("ceiling_denied detail = %q, want path auto-resume and the exec refusal", detail)
	}
}

// TestFallbackStopsOnCeilingDenied: a failover hands the failed work to another AGENT,
// which this caller's ceiling does not include. The transfer is not submitted, the
// failure stays terminal and the source job records job.ceiling_denied path=fallback.
func TestFallbackStopsOnCeilingDenied(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := withCeilingOnCfg(
		fallbackCfg(t, root, "ERROR: Selected model is at capacity. Please try a different model."), "ci-bot",
		func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
			c.Agents = []string{"codex"} // the candidate "omp" is out of scope
		})
	s := newServiceFromCfg(t, root, cfg)

	// No session: nothing can continue the failed agent, so the decision is the transfer.
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local", OriginCaller: "ci-bot",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed", src.Status)
	}
	types := waitForJobEvents(t, s, src.ID, EventJobCeilingDenied, EventJobTerminal)
	if !hasSubsequence(types, []string{EventJobCeilingDenied, EventJobTerminal}) {
		t.Fatalf("events = %v, want the refusal before the terminal event", types)
	}
	if got, _ := s.Get(src.ID); got.FellBackTo != "" {
		t.Fatalf("fell_back_to = %q, want none (the takeover was refused)", got.FellBackTo)
	}
	if detail := ceilingDeniedDetail(t, s, src.ID, EventJobCeilingDenied); !strings.Contains(detail, `"path":"fallback"`) {
		t.Fatalf("ceiling_denied detail = %q, want path fallback", detail)
	}
}

// TestWakeupStopsOnCeilingDenied: a wakeup whose continuation the job owner's ceiling
// refuses is CONSUMED (disabled) — a repeating timer would otherwise fire the same
// refusal on every tick — and the refusal is on the job's timeline.
func TestWakeupStopsOnCeilingDenied(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := autoResumeCfg(testcmd.Path(t), root, "codex", "", nil)
	ac := cfg.Agents["codex"]
	ac.Args = []string{"printf", "done {{prompt}}"} // this run succeeds; the CONTINUATION is what gets refused
	cfg.Agents["codex"] = ac
	cfg = withCeilingOnCfg(cfg, "ci-bot", func(c *config.CallerCeiling) {
		c.Projects = []string{"self"}
		c.Runners = []string{"local"}
		c.Agents = []string{"codex"}
		// No AllowExec: the resume carrier's argv is out of scope.
	})
	s := newServiceFromCfg(t, root, cfg)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local", CallerID: "ci-bot", OriginCaller: "ci-bot",
		Prompt: "first turn", Cwd: ".", TimeoutSec: 30, SessionID: "sess-wk",
	})
	if src.Status != StatusDone {
		t.Fatalf("setup: status = %s (err=%s), want done", src.Status, src.Error)
	}
	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvery, EverySec: 60, Instruction: "again please",
	}, "ci-bot", AllowAllWakeups)
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}
	s.FireWakeup(w.ID, jobstore.WakeupKindEvery)

	got, ok, err := s.GetWakeup(w.ID)
	if err != nil || !ok {
		t.Fatalf("GetWakeup(%s): ok=%v err=%v", w.ID, ok, err)
	}
	if got.Enabled != 0 {
		t.Fatalf("wakeup enabled = %d, want 0 (a permanently refused wakeup must not re-arm)", got.Enabled)
	}
	waitForJobEvents(t, s, src.ID, EventJobWakeupFailed, EventJobCeilingDenied)
	if detail := ceilingDeniedDetail(t, s, src.ID, EventJobCeilingDenied); !strings.Contains(detail, `"path":"wakeup"`) {
		t.Fatalf("ceiling_denied detail = %q, want path wakeup", detail)
	}
	if detail := ceilingDeniedDetail(t, s, src.ID, EventJobWakeupFailed); !strings.Contains(detail, w.ID) {
		t.Fatalf("wakeup_failed detail = %q, want the wakeup id", detail)
	}
}

// TestTodoDispatchCeilingDeniedWritesDispatchError: a plan item dispatched for a
// restricted caller (the item's assignee is out of its ceiling) starts nothing, keeps its
// status and stores the refusal as the item's dispatch_error (design §准入: todo 派发).
func TestTodoDispatchCeilingDeniedWritesDispatchError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newDispatchService(t, root)
	reloadCeiling(t, s, "ci-bot", func(c *config.CallerCeiling) {
		c.Projects = []string{"self"}
		c.Runners = []string{"local"}
		c.Agents = []string{"other"}
	})
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "pl-ceil", Title: "plan", ProjectKey: "self"},
		jobstore.PlanTodo{TodoID: "td-ceil", Title: "item", Assignee: "omp", Status: jobstore.TodoReady})

	disp, err := s.DispatchTodo("td-ceil", "ci-bot")
	if err != nil {
		t.Fatalf("DispatchTodo: %v", err)
	}
	if disp.Job != nil {
		t.Fatalf("a denied dispatch started job %s", disp.Job.ID)
	}
	rec, ok, err := s.Meta().GetTodo("td-ceil")
	if err != nil || !ok {
		t.Fatalf("GetTodo: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(rec.DispatchError, "ceiling") {
		t.Fatalf("dispatch_error = %q, want the ceiling refusal", rec.DispatchError)
	}
	if rec.Status != jobstore.TodoReady {
		t.Fatalf("todo status = %q, want it kept at %q", rec.Status, jobstore.TodoReady)
	}
	if jobs, _ := s.Meta().ListJobs(jobstore.ListQuery{}); len(jobs) != 0 {
		t.Fatalf("a denied dispatch created %d job(s)", len(jobs))
	}
}

// TestWorkerLabelAutoSelectDeniedByCeiling: a worker the LABEL auto-select picked (the
// request never named it) is still judged by the owner's ceiling — a ceiling that lists
// workers must bound the one the job actually lands on (design 2026-10-18 §准入).
func TestWorkerLabelAutoSelectDeniedByCeiling(t *testing.T) {
	t.Parallel()
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{
		"w1": {Token: "tok-w1"},
		"w2": {Token: "tok-w2"},
	}
	sel := fakeSelector{cands: []WorkerCandidate{
		selfCaps(WorkerCandidate{WorkerID: "w1", Labels: []string{"cpu"}, InFlight: 3, HeartbeatAge: time.Second}),
		selfCaps(WorkerCandidate{WorkerID: "w2", Labels: []string{"gpu"}, InFlight: 0, HeartbeatAge: time.Second}),
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)
	reloadCeiling(t, s, "ci-bot", func(c *config.CallerCeiling) {
		c.Projects = []string{"self"}
		c.Runners = []string{"pool-w"}
		c.Agents = []string{"exec"}
		c.AllowExec = boolPtr(true)
		c.Workers = []string{"w1"} // the label "gpu" selects w2 instead
	})

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "pool-w", WorkerLabels: []string{"gpu"},
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30, OriginCaller: "ci-bot",
	})
	if !errors.Is(err, ErrCeilingDenied) {
		t.Fatalf("err = %v, want ErrCeilingDenied for the auto-selected worker", err)
	}
	if stub.gotForward != nil {
		t.Fatalf("a denied submit was dispatched: %+v", stub.gotForward)
	}
}
