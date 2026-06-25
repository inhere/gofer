package workflow

import (
	"path/filepath"
	"testing"
	"time"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// retryFailStep builds an exec step that always exits non-zero, with the given
// on_failure=retry policy (max attempts + backoff seconds). cwd "." resolves to the
// project root so the step is deterministic.
func retryFailStep(name string, max int, backoff []int) StepSpec {
	return StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
		OnFailure: onFailureRetry,
		Retry:     &job.RetryPolicy{MaxAttempts: max, BackoffSec: backoff},
	}
}

// flakyStep builds an exec step that FAILS on its first run and SUCCEEDS on every
// later run, gated by a marker file under dir. The first invocation creates the
// marker and exits 7; subsequent invocations see the marker and exit 0. This drives
// "retry then succeed" without driving the clock (backoff 0 => immediate).
func flakyStep(name, dir string, max int) StepSpec {
	marker := filepath.Join(dir, "flaky-"+name+".marker")
	// `test -f X || (touch X; exit 7)` : first run misses X, creates it, exits 7;
	// every later run finds X and the `||` short-circuits to exit 0.
	script := "test -f " + marker + " || { touch " + marker + "; exit 7; }"
	return StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", script}, Cwd: ".", TimeoutSec: 30,
		OnFailure: onFailureRetry,
		Retry:     &job.RetryPolicy{MaxAttempts: max, BackoffSec: []int{0}}, // immediate retry
	}
}

// continueFailStep builds an always-failing step with on_failure=continue.
func continueFailStep(name string) StepSpec {
	return StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
		OnFailure: onFailureContinue,
	}
}

// ---------------------------------------------------------------------------
// D23 backward compatibility: a v1 spec (no on_failure / no retry) runs unchanged.
// ---------------------------------------------------------------------------

// TestV1SpecBackwardCompatible is the D23 regression: a workflow built from a v1
// spec (no on_failure, no retry — the zero-value StepSpec extension) runs exactly as
// before — happy path completes done, and a failing step still fail-fasts. This is
// the回归底线.
func TestV1SpecBackwardCompatible(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

	// Happy path: 3 plain echo steps -> done, no new fields touched.
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{echoStep("a"), echoStep("b"), echoStep("c")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("v1 happy-path workflow = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.StepAttempt != 1 {
		t.Fatalf("v1 workflow step_attempt = %d, want 1 (no retry touched)", final.StepAttempt)
	}

	// Fail-fast path: a plain failing step (no on_failure) still fails the workflow
	// and suppresses later steps — identical to v1.
	wf2, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{echoStep("ok1"), failStep("boom2"), echoStep("never3")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow(failfast): %v", err)
	}
	final2 := waitWorkflow(t, e, wf2.ID)
	if final2.Status != jobstore.WorkflowFailed {
		t.Fatalf("v1 fail-fast workflow = %s, want failed", final2.Status)
	}
	jobs, _ := e.meta.ListWorkflowJobs(wf2.ID)
	for _, j := range jobs {
		if j.StepIndex == 3 {
			t.Fatalf("step 3 ran despite v1 fail-fast (job %s)", j.ID)
		}
		// Every v1 step-job carries attempt 1 (the deterministic request_id a1 segment).
		if j.Attempt != 1 {
			t.Fatalf("step %d attempt = %d, want 1", j.StepIndex, j.Attempt)
		}
	}
}

// ---------------------------------------------------------------------------
// on_failure: fail / continue / retry — three states.
// ---------------------------------------------------------------------------

// TestOnFailureFail asserts on_failure=fail (explicit) behaves as v1 fail-fast.
func TestOnFailureFail(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	failExplicit := failStep("boom")
	failExplicit.OnFailure = onFailureFail
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{echoStep("ok1"), failExplicit, echoStep("never3")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("on_failure=fail workflow = %s, want failed", final.Status)
	}
	jobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	if len(jobs) != 2 {
		t.Fatalf("ran %d step jobs, want 2 (step3 suppressed)", len(jobs))
	}
}

// TestOnFailureContinue asserts on_failure=continue skips the failed step and runs
// the next, completing the workflow done.
func TestOnFailureContinue(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{echoStep("ok1"), continueFailStep("boom2"), echoStep("ok3")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("on_failure=continue workflow = %s (err=%s), want done", final.Status, final.Error)
	}
	jobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	if len(jobs) != 3 {
		t.Fatalf("ran %d step jobs, want 3 (failed step2 skipped, step3 ran)", len(jobs))
	}
	if step3 := stepJobAttempt(jobs, 3, 1); step3 == nil || step3.Status != job.StatusDone {
		t.Fatalf("step3 should have run and be done after skip, got %+v", step3)
	}
	// A skipped event is recorded.
	if !hasWorkflowEvent(t, e, wf.ID, job.EventStepSkipped) {
		t.Fatal("expected a step.skipped event for on_failure=continue")
	}
}

// TestOnFailureContinueLastStep asserts skipping the LAST step still completes the
// workflow done (the boundary where there is no next step).
func TestOnFailureContinueLastStep(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{echoStep("ok1"), continueFailStep("boomLast")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("skip-last workflow = %s, want done", final.Status)
	}
}

// TestOnFailureRetryThenSucceed asserts a flaky step that fails once then succeeds
// is retried (immediate backoff) and the workflow completes done — with TWO
// attempts recorded at the same step_index.
func TestOnFailureRetryThenSucceed(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{flakyStep("f", root, 3), echoStep("done")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("retry-then-succeed workflow = %s (err=%s), want done", final.Status, final.Error)
	}
	jobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	// Step 1: attempt 1 (failed) + attempt 2 (done). Step 2: attempt 1 (done).
	a1 := stepJobAttempt(jobs, 1, 1)
	a2 := stepJobAttempt(jobs, 1, 2)
	if a1 == nil || a2 == nil {
		t.Fatalf("expected step1 attempts 1 and 2, got jobs=%+v", jobs)
	}
	if a1.Status != job.StatusFailed {
		t.Fatalf("step1 attempt1 = %s, want failed", a1.Status)
	}
	if a2.Status != job.StatusDone {
		t.Fatalf("step1 attempt2 = %s, want done", a2.Status)
	}
	// A step.retry event was recorded.
	if !hasWorkflowEvent(t, e, wf.ID, job.EventStepRetry) {
		t.Fatal("expected a step.retry event")
	}
}

// TestOnFailureRetryExhausted asserts a step that always fails, after exhausting
// MaxAttempts, fails the whole workflow — with exactly MaxAttempts attempts run.
func TestOnFailureRetryExhausted(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{retryFailStep("always", 3, []int{0})},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("retry-exhausted workflow = %s, want failed", final.Status)
	}
	jobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	// 3 attempts at step 1, all failed.
	if len(jobs) != 3 {
		t.Fatalf("ran %d step1 attempts, want 3 (MaxAttempts)", len(jobs))
	}
	for _, j := range jobs {
		if j.StepIndex != 1 {
			t.Fatalf("unexpected step_index %d", j.StepIndex)
		}
		if j.Status != job.StatusFailed {
			t.Fatalf("attempt %d = %s, want failed", j.Attempt, j.Status)
		}
	}
}

// TestRetryOnExitCodesNotMatched asserts on_exit_codes restricts retry: a step that
// exits 7 with on_exit_codes=[42] is NOT retried (fail-fast immediately).
func TestRetryOnExitCodesNotMatched(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	step := retryFailStep("exit7", 3, []int{0})
	step.Retry.OnExitCodes = []int{42} // 7 is not in the list -> not retryable
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{step}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow = %s, want failed (exit 7 not in on_exit_codes)", final.Status)
	}
	jobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	if len(jobs) != 1 {
		t.Fatalf("ran %d attempts, want 1 (no retry: exit code not matched)", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// Retry backoff increases with attempt (injected nowFn verifies next_step_at).
// ---------------------------------------------------------------------------

// TestBackoffForPolicyIncreasesByAttempt is the pure-helper proof that the退避
// indexes the table by the just-failed attempt (1-based) and clamps to the last
// entry past the end (SR606), so the wait strictly increases across attempts.
func TestBackoffForPolicyIncreasesByAttempt(t *testing.T) {
	p := &job.RetryPolicy{MaxAttempts: 5, BackoffSec: []int{5, 50, 500}}
	cases := []struct {
		attempt int
		want    int
	}{
		{1, 5}, {2, 50}, {3, 500}, {4, 500}, {5, 500}, // clamp to last past the end
	}
	var prev int
	for _, tc := range cases {
		got := job.BackoffForPolicy(p, tc.attempt)
		if got != tc.want {
			t.Fatalf("backoff(attempt %d) = %d, want %d", tc.attempt, got, tc.want)
		}
		if tc.attempt <= 3 && got <= prev {
			t.Fatalf("backoff did not increase at attempt %d: %d <= %d", tc.attempt, got, prev)
		}
		prev = got
	}
	// Default (no BackoffSec) follows the SR606 table.
	if got := job.BackoffForPolicy(&job.RetryPolicy{}, 1); got != 30 {
		t.Fatalf("default backoff[0] = %d, want %d", got, 30)
	}
}

// TestRetryScheduleNextStepAt is the integration proof that a step's FIRST failure
// schedules next_step_at = now + backoff[0] (the退避到点时间) with the clock pinned,
// so the wait is observable end-to-end. (The per-attempt increase is covered as a
// pure helper above; chaining the full clock-advanced retry sequence is inherently
// racy with the async finish hook, so it is split out.)
func TestRetryScheduleNextStepAt(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	const base = int64(1_000_000)
	clk := &fixedClock{}
	clk.set(base)
	e.now = func() time.Time { return time.Unix(clk.now(), 0) }

	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{retryFailStep("always", 3, []int{42, 4200})},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	waitStepJobTerminal(t, e, wf.ID, 1) // attempt 1 fails
	got := waitStepAttempt(t, e, wf.ID, 2)
	// First failure (attempt 1) indexes backoff[0]=42, so next_step_at = base+42.
	if got.NextStepAt != base+42 {
		t.Fatalf("first-failure next_step_at = %d, want base+42 (=%d)", got.NextStepAt, base+42)
	}
	if got.StepAttempt != 2 {
		t.Fatalf("step_attempt = %d, want 2 after one retry", got.StepAttempt)
	}
	// Cancel to drain (the 42s real timer would otherwise outlive the test).
	_ = e.CancelWorkflow(wf.ID)
}

// ---------------------------------------------------------------------------
// ⭐ Concurrency hard test: finish+sweeper racing must start ONE att+1 job and
// transition state exactly once.
// ---------------------------------------------------------------------------

// TestRetryConcurrentSingleAttemptJob is the P1 ⭐ correctness test (plan §并发硬测):
// after a retryable step fails, MANY concurrent Advance calls (simulating
// the finish hook + the sweeper + duplicates firing together) must schedule the
// retry exactly once and, once due, start EXACTLY ONE attempt-2 job — never two at
// the same (step, attempt). It proves the AdvanceStep二元组抢权 + deterministic
// request_id double-safeguard holds under concurrency.
func TestRetryConcurrentSingleAttemptJob(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	const nowUnix = int64(2_000_000)
	clk := &fixedClock{}
	clk.set(nowUnix)
	e.now = func() time.Time { return time.Unix(clk.now(), 0) }

	// One always-failing step, max 3 attempts, non-trivial backoff so the retry is
	// scheduled (not immediately started) — leaving a clean window to hammer advance.
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{retryFailStep("always", 3, []int{30})},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	// Wait for attempt-1's job to FAIL.
	waitStepJobTerminal(t, e, wf.ID, 1)
	// Wait for the retry to be SCHEDULED (the finish hook fires advance -> (1,1)->(1,2)
	// with next_step_at in the future); only then is the hammer below racing a clean
	// already-scheduled state.
	waitStepAttempt(t, e, wf.ID, 2)

	// HAMMER Advance with the clock still BEFORE the backoff is due: the
	// retry transition already happened; every hammer call must be a no-op (next
	// attempt not yet due) — nobody starts the attempt-2 job yet.
	hammer(e, wf.ID, 32)

	got, _, _ := e.meta.GetWorkflow(wf.ID)
	if got.StepAttempt != 2 {
		t.Fatalf("after retry, step_attempt = %d, want 2 (transition ran exactly once)", got.StepAttempt)
	}
	if got.NextStepAt != nowUnix+30 {
		t.Fatalf("next_step_at = %d, want now+30 (=%d)", got.NextStepAt, nowUnix+30)
	}
	// Still only ONE job (attempt 1); attempt-2 job not started (backoff pending).
	jobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	if len(jobs) != 1 {
		t.Fatalf("after retry-schedule, %d jobs, want 1 (attempt-2 not yet started)", len(jobs))
	}

	// Advance the clock past the backoff and HAMMER again: now exactly ONE attempt-2
	// job must be started, never two — the C5 request_id idempotency holds under the
	// concurrent finish+sweeper race. Poll until the attempt-2 job appears, then assert
	// the single-job invariant.
	clk.set(nowUnix + 31)
	hammer(e, wf.ID, 32)
	a2 := waitStepAttemptJob(t, e, wf.ID, 1, 2) // attempt-2 job started exactly once

	// CRITICAL invariant: across ALL jobs of this workflow, no (step, attempt) pair
	// started more than once — the double-safeguard (AdvanceStep二元组 + deterministic
	// request_id) held under the finish+sweeper+duplicate storm.
	jobs, _ = e.meta.ListWorkflowJobs(wf.ID)
	seen := map[[2]int]int{}
	for _, j := range jobs {
		seen[[2]int{j.StepIndex, j.Attempt}]++
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("(step %d, attempt %d) started %d times, want exactly 1 (幂等 violated)", key[0], key[1], count)
		}
	}
	// Exactly two attempt jobs exist at step 1 (attempt 1 failed + attempt 2 started),
	// proving the retry storms produced ONE attempt-2 job, not many.
	if a2 == nil {
		t.Fatal("attempt-2 job was not started")
	}
	step1Count := 0
	for _, j := range jobs {
		if j.StepIndex == 1 {
			step1Count++
		}
	}
	if step1Count != 2 {
		t.Fatalf("step 1 has %d attempt jobs, want 2 (attempt 1 + the single attempt 2)", step1Count)
	}

	// Drain: hand the chain to completion so background goroutines settle before the
	// test store closes. Bump the clock well past any backoff and let it run out.
	clk.set(nowUnix + 1_000_000)
	for i := 0; i < 5; i++ {
		e.Advance(wf.ID)
		if wf, _, _ := e.meta.GetWorkflow(wf.ID); wf.Status != jobstore.WorkflowRunning {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitStepAttemptJob polls until the (step, attempt) job exists and returns it (or
// fails). Used to confirm a retried attempt's single job was started.
func waitStepAttemptJob(t *testing.T, e *Engine, wfID string, step, attempt int) *jobstore.JobRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := e.meta.ListWorkflowJobs(wfID)
		if j := stepJobAttempt(jobs, step, attempt); j != nil {
			return j
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("(step %d, attempt %d) job did not appear in time", step, attempt)
	return nil
}

// hammer fires n concurrent Advance calls and waits for all to return —
// the finish-hook + sweeper + duplicate storm simulation.
func hammer(e *Engine, wfID string, n int) {
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() { defer func() { done <- struct{}{} }(); e.Advance(wfID) }()
	}
	for i := 0; i < n; i++ {
		<-done
	}
}

// ---------------------------------------------------------------------------
// workflow_events timeline ordering.
// ---------------------------------------------------------------------------

// TestWorkflowEventsTimeline asserts a retry workflow records a correctly-ordered
// event timeline: submitted -> step.started(1) -> step.retry -> step.started(2 of
// step1 OR step2...) -> ... -> workflow.terminal, in seq order.
func TestWorkflowEventsTimeline(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{flakyStep("f", root, 3), echoStep("two")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow = %s, want done", final.Status)
	}

	events, err := e.ListWorkflowEvents(wf.ID, 0)
	if err != nil {
		t.Fatalf("ListWorkflowEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no workflow events recorded")
	}
	// seq must be strictly increasing (append-only insertion order).
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("events not seq-ordered: [%d].seq=%d <= [%d].seq=%d", i, events[i].Seq, i-1, events[i-1].Seq)
		}
	}
	// First event is workflow.submitted; last is workflow.terminal.
	if events[0].Type != job.EventWorkflowSubmitted {
		t.Fatalf("first event = %s, want %s", events[0].Type, job.EventWorkflowSubmitted)
	}
	if last := events[len(events)-1]; last.Type != job.EventWorkflowTerminal {
		t.Fatalf("last event = %s, want %s", last.Type, job.EventWorkflowTerminal)
	}
	// The expected types appear in order: submitted, started, retry, ... terminal.
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	if !containsInOrder(types, []string{
		job.EventWorkflowSubmitted, job.EventStepStarted, job.EventStepRetry, job.EventWorkflowTerminal,
	}) {
		t.Fatalf("event timeline missing expected ordered types, got: %v", types)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// waitStepAttempt polls until the workflow's step_attempt reaches want, returning
// the header snapshot at that point.
func waitStepAttempt(t *testing.T, e *Engine, wfID string, want int) jobstore.Workflow {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		wf, ok, _ := e.meta.GetWorkflow(wfID)
		if ok && wf.StepAttempt >= want {
			return wf
		}
		if ok && wf.Status != jobstore.WorkflowRunning {
			t.Fatalf("workflow %s reached %s before step_attempt %d", wfID, wf.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workflow %s did not reach step_attempt %d in time", wfID, want)
	return jobstore.Workflow{}
}

// hasWorkflowEvent reports whether the workflow recorded an event of the given type.
func hasWorkflowEvent(t *testing.T, e *Engine, wfID, eventType string) bool {
	t.Helper()
	events, err := e.ListWorkflowEvents(wfID, 0)
	if err != nil {
		t.Fatalf("ListWorkflowEvents: %v", err)
	}
	for _, e := range events {
		if e.Type == eventType {
			return true
		}
	}
	return false
}

// containsInOrder reports whether want appears as an ordered (not necessarily
// contiguous) subsequence of got.
func containsInOrder(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}
