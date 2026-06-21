package job

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// waitWorkflow polls until the workflow reaches a terminal status (done/failed/
// cancelled) or the deadline elapses. The chain runs asynchronously (each step is
// a background job + the finish hook fires advanceWorkflow), so tests poll the
// persisted header rather than block on a single channel.
func waitWorkflow(t *testing.T, s *Service, wfID string) jobstore.Workflow {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		wf, ok, err := s.meta.GetWorkflow(wfID)
		if err != nil {
			t.Fatalf("GetWorkflow: %v", err)
		}
		if ok && wf.Status != jobstore.WorkflowRunning {
			return wf
		}
		time.Sleep(15 * time.Millisecond)
	}
	wf, _, _ := s.meta.GetWorkflow(wfID)
	t.Fatalf("workflow %s did not finish in time (status=%s, step=%d)", wfID, wf.Status, wf.CurrentStep)
	return jobstore.Workflow{}
}

// echoStep builds a fast exec step that succeeds (exit 0).
func echoStep(name string) StepSpec {
	return StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "echo " + name}, Cwd: ".", TimeoutSec: 30,
	}
}

// failStep builds an exec step that exits non-zero (fail-fast trigger).
func failStep(name string) StepSpec {
	return StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
	}
}

// TestSubmitWorkflowStartsFirstStep asserts a 3-step workflow creates a running
// header (current_step=1, total=3) and starts ONLY step 1 (the rest wait).
func TestSubmitWorkflowStartsFirstStep(t *testing.T) {
	s := newTestService(t, t.TempDir())
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Title: "chain",
		Steps: []StepSpec{echoStep("a"), echoStep("b"), echoStep("c")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if wf.Status != jobstore.WorkflowRunning {
		t.Fatalf("status = %s, want running", wf.Status)
	}
	if wf.CurrentStep != 1 || wf.TotalSteps != 3 {
		t.Fatalf("current/total = %d/%d, want 1/3", wf.CurrentStep, wf.TotalSteps)
	}
	if wf.CallerID != "alice" {
		t.Fatalf("caller = %q, want alice", wf.CallerID)
	}

	// Step-1 job exists, bound to step_index=1 and inheriting the caller.
	jobs, err := s.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("started %d step jobs, want exactly 1 (step 1)", len(jobs))
	}
	if jobs[0].StepIndex != 1 {
		t.Fatalf("step_index = %d, want 1", jobs[0].StepIndex)
	}
	if jobs[0].WorkflowID != wf.ID {
		t.Fatalf("workflow_id = %q, want %q", jobs[0].WorkflowID, wf.ID)
	}
	if jobs[0].CallerID != "alice" {
		t.Fatalf("step caller = %q, want alice (D8 inherit)", jobs[0].CallerID)
	}

	// Drain the async step-1 job so its background goroutine finishes before the
	// test's store is closed (avoids best-effort recordEvent racing teardown).
	s.Wait(jobs[0].ID)
}

// TestSubmitWorkflowEmptySteps asserts an empty spec is rejected.
func TestSubmitWorkflowEmptySteps(t *testing.T) {
	s := newTestService(t, t.TempDir())
	_, err := s.SubmitWorkflow(WorkflowSpec{Steps: nil}, "alice")
	if err == nil {
		t.Fatal("expected error for empty steps")
	}
}

// TestWorkflowRunsAllStepsSerially is the推进 happy-path: a 3-step echo chain runs
// to completion via the real finish hook — each step done in order, the workflow
// done, and step indices 1->2->3 (one job per step, ascending).
func TestWorkflowRunsAllStepsSerially(t *testing.T) {
	s := newTestService(t, t.TempDir())
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{echoStep("one"), echoStep("two"), echoStep("three")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s (err=%s), want done", final.Status, final.Error)
	}
	// current_step advances past the last step (N -> N+1) because EVERY terminal
	// step — including the last — goes through one AdvanceCurrentStep to win the
	// 推进权 (so duplicate final-step callers can't double-fire the done transition).
	if final.CurrentStep != final.TotalSteps+1 {
		t.Fatalf("current_step = %d, want %d (TotalSteps+1)", final.CurrentStep, final.TotalSteps+1)
	}

	jobs, err := s.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("ran %d step jobs, want exactly 3", len(jobs))
	}
	for i, j := range jobs {
		if j.StepIndex != i+1 {
			t.Fatalf("job[%d] step_index = %d, want %d", i, j.StepIndex, i+1)
		}
		if j.Status != StatusDone {
			t.Fatalf("step %d status = %s, want done", j.StepIndex, j.Status)
		}
	}
}

// TestAdvanceWorkflowIdempotentConcurrent is the幂等 core (一个 step 绝不起两次):
// after step 1 finishes, many concurrent advanceWorkflow calls (simulating the
// finish hook + sweeper firing together, plus duplicates) must start the next
// step EXACTLY ONCE — never two jobs at the same step_index.
func TestAdvanceWorkflowIdempotentConcurrent(t *testing.T) {
	s := newTestService(t, t.TempDir())
	// A 3-step chain where step 1 finishes fast; we then hammer advance manually.
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{echoStep("s1"), echoStep("s2"), echoStep("s3")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	// Wait for step 1's job to reach terminal WITHOUT letting the chain run away:
	// grab the step-1 job id and Wait on it. (The finish hook will also fire
	// advance, but that is part of what we are proving is safe.)
	waitStepJobTerminal(t, s, wf.ID, 1)

	// Hammer advanceWorkflow concurrently (finish hook + sweeper + duplicates).
	const n = 24
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() { defer func() { done <- struct{}{} }(); s.advanceWorkflow(wf.ID) }()
	}
	for i := 0; i < n; i++ {
		<-done
	}

	// After the dust settles the workflow runs to completion; crucially each
	// step_index has at most ONE job (no double-start).
	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s (err=%s), want done", final.Status, final.Error)
	}
	jobs, err := s.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	seen := map[int]int{}
	for _, j := range jobs {
		seen[j.StepIndex]++
	}
	for step, count := range seen {
		if count != 1 {
			t.Fatalf("step %d started %d times, want exactly 1 (幂等 violated)", step, count)
		}
	}
	if len(jobs) != 3 {
		t.Fatalf("total step jobs = %d, want 3", len(jobs))
	}
}

// TestWorkflowFailFast asserts a failing step stops the chain: step 2 exits non-
// zero -> step 3 never starts and the workflow is failed.
func TestWorkflowFailFast(t *testing.T) {
	s := newTestService(t, t.TempDir())
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{echoStep("ok1"), failStep("boom2"), echoStep("never3")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow status = %s, want failed", final.Status)
	}
	jobs, err := s.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	// Steps 1 and 2 ran; step 3 must NOT have started.
	for _, j := range jobs {
		if j.StepIndex == 3 {
			t.Fatalf("step 3 started despite fail-fast (job %s)", j.ID)
		}
	}
	if len(jobs) != 2 {
		t.Fatalf("ran %d step jobs, want 2 (step3 suppressed)", len(jobs))
	}
}

// TestCancelWorkflow asserts CancelWorkflow on a running workflow marks it
// cancelled, cancels the current step's job, and starts no further steps.
func TestCancelWorkflow(t *testing.T) {
	s := newTestService(t, t.TempDir())
	// Step 1 sleeps so the workflow is still on step 1 when we cancel.
	sleepStep := StepSpec{
		Name: "sleep1", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "10"}, Cwd: ".", TimeoutSec: 30,
	}
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{sleepStep, echoStep("two"), echoStep("three")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	// Ensure step 1 is actually running before cancelling.
	waitStepJobRunning(t, s, wf.ID, 1)

	if err := s.CancelWorkflow(wf.ID); err != nil {
		t.Fatalf("CancelWorkflow: %v", err)
	}

	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowCancelled {
		t.Fatalf("workflow status = %s, want cancelled", final.Status)
	}
	jobs, err := s.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	for _, j := range jobs {
		if j.StepIndex >= 2 {
			t.Fatalf("step %d started after cancel (job %s)", j.StepIndex, j.ID)
		}
	}
	// Cancel is idempotent (second call is a no-op).
	if err := s.CancelWorkflow(wf.ID); err != nil {
		t.Fatalf("second CancelWorkflow: %v", err)
	}

	// Drain the cancelled step-1 job so its background goroutine finishes before
	// the test store is closed (best-effort recordEvent vs teardown).
	waitStepJobTerminal(t, s, wf.ID, 1)
}

// waitStepJobTerminal waits for the workflow's step-N job to reach terminal and
// returns it.
func waitStepJobTerminal(t *testing.T, s *Service, wfID string, step int) jobstore.JobRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := s.meta.ListWorkflowJobs(wfID)
		if j := stepJob(jobs, step); j != nil && isTerminal(j.Status) {
			return *j
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("step %d of %s did not reach terminal in time", step, wfID)
	return jobstore.JobRecord{}
}

// waitStepJobRunning waits for the workflow's step-N job to be running.
func waitStepJobRunning(t *testing.T, s *Service, wfID string, step int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := s.meta.ListWorkflowJobs(wfID)
		if j := stepJob(jobs, step); j != nil && j.Status == StatusRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("step %d of %s did not start running in time", step, wfID)
}

// TestAdvanceRunningWorkflowsRecoversCrashedAdvance simulates a crash: a running
// workflow whose step-1 job reached done but whose finish-hook advance never ran
// (the next step was never started). The sweeper (AdvanceRunningWorkflows) must
// re-drive it and start step 2.
func TestAdvanceRunningWorkflowsRecoversCrashedAdvance(t *testing.T) {
	s := newTestService(t, t.TempDir())

	// Build the spec we want (2 echo steps) and persist a running workflow header
	// + a DONE step-1 job DIRECTLY via the store, bypassing the finish hook — this
	// is exactly the "advance was lost" state a crash leaves behind.
	spec := WorkflowSpec{Steps: []StepSpec{echoStep("s1"), echoStep("s2")}}
	wfID := "wf-crash"
	specJSON := mustMarshalSpec(t, spec)
	if err := s.meta.InsertWorkflow(jobstore.Workflow{
		ID: wfID, Status: jobstore.WorkflowRunning, CurrentStep: 1, TotalSteps: 2,
		SpecJSON: specJSON, CallerID: "alice", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("InsertWorkflow: %v", err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "crash-step1", ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: StatusDone, ResultDir: "/tmp/x/crash-step1", StartedAt: 1,
		WorkflowID: wfID, StepIndex: 1,
	}); err != nil {
		t.Fatalf("UpsertJob step1: %v", err)
	}

	// Before the sweep: only step 1 exists.
	jobs, _ := s.meta.ListWorkflowJobs(wfID)
	if len(jobs) != 1 {
		t.Fatalf("pre-sweep step jobs = %d, want 1", len(jobs))
	}

	// Sweep: must recover and start step 2.
	n := s.AdvanceRunningWorkflows(context.Background())
	if n != 1 {
		t.Fatalf("AdvanceRunningWorkflows inspected %d, want 1 running", n)
	}

	final := waitWorkflow(t, s, wfID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s, want done after recovery", final.Status)
	}
	jobs, _ = s.meta.ListWorkflowJobs(wfID)
	if len(jobs) != 2 {
		t.Fatalf("post-sweep step jobs = %d, want 2 (step2 recovered)", len(jobs))
	}
	if stepJob(jobs, 2) == nil {
		t.Fatal("step 2 was not started by the sweeper")
	}
}

// TestAdvanceRunningWorkflowsNoOpWhenEmpty asserts the sweeper is a clean no-op
// when there are no running workflows.
func TestAdvanceRunningWorkflowsNoOpWhenEmpty(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if n := s.AdvanceRunningWorkflows(context.Background()); n != 0 {
		t.Fatalf("sweep inspected %d, want 0 (no running workflows)", n)
	}
}

// mustMarshalSpec marshals a WorkflowSpec exactly as SubmitWorkflow does.
func mustMarshalSpec(t *testing.T, spec WorkflowSpec) string {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return string(b)
}

// TestSubmitWorkflowRejectsInvalidStep asserts every step is validated at submit
// time: an invalid project/agent/runner in ANY step fails the whole submit before
// any job starts.
func TestSubmitWorkflowRejectsInvalidStep(t *testing.T) {
	s := newTestService(t, t.TempDir())

	cases := []struct {
		name  string
		steps []StepSpec
	}{
		{"unknown project", []StepSpec{echoStep("ok"), {Name: "bad", ProjectKey: "ghost", Agent: "exec", Runner: "local", Cmd: []string{"true"}}}},
		{"exec not allowed", []StepSpec{{Name: "bad", ProjectKey: "noexec", Agent: "exec", Runner: "local", Cmd: []string{"true"}}}},
		{"missing runner", []StepSpec{{Name: "bad", ProjectKey: "self", Agent: "exec", Runner: ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, err := s.SubmitWorkflow(WorkflowSpec{Steps: tc.steps}, "alice")
			if err == nil {
				t.Fatalf("expected rejection, got workflow %s", wf.ID)
			}
		})
	}
}
