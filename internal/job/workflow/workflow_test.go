package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// waitWorkflow polls until the workflow reaches a terminal status (done/failed/
// cancelled) or the deadline elapses. The chain runs asynchronously (each step is
// a background job + the finish hook fires Advance), so tests poll the
// persisted header rather than block on a single channel.
func waitWorkflow(t *testing.T, e *Engine, wfID string) jobstore.Workflow {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		wf, ok, err := e.meta.GetWorkflow(wfID)
		if err != nil {
			t.Fatalf("GetWorkflow: %v", err)
		}
		if ok && wf.Status != jobstore.WorkflowRunning {
			return wf
		}
		time.Sleep(15 * time.Millisecond)
	}
	wf, _, _ := e.meta.GetWorkflow(wfID)
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
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
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
	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
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
	e.ops.Wait(jobs[0].ID)
}

// TestSubmitWorkflowEmptySteps asserts an empty spec is rejected.
func TestSubmitWorkflowEmptySteps(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	_, err := e.SubmitWorkflow(Spec{Steps: nil}, "alice")
	if err == nil {
		t.Fatal("expected error for empty steps")
	}
}

// TestWorkflowRunsAllStepsSerially is the推进 happy-path: a 3-step echo chain runs
// to completion via the real finish hook — each step done in order, the workflow
// done, and step indices 1->2->3 (one job per step, ascending).
func TestWorkflowRunsAllStepsSerially(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{echoStep("one"), echoStep("two"), echoStep("three")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s (err=%s), want done", final.Status, final.Error)
	}
	// current_step advances past the last step (N -> N+1) because EVERY terminal
	// step — including the last — goes through one AdvanceCurrentStep to win the
	// 推进权 (so duplicate final-step callers can't double-fire the done transition).
	if final.CurrentStep != final.TotalSteps+1 {
		t.Fatalf("current_step = %d, want %d (TotalSteps+1)", final.CurrentStep, final.TotalSteps+1)
	}

	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
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
		if j.Status != job.StatusDone {
			t.Fatalf("step %d status = %s, want done", j.StepIndex, j.Status)
		}
	}
}

// TestAdvanceWorkflowIdempotentConcurrent is the幂等 core (一个 step 绝不起两次):
// after step 1 finishes, many concurrent Advance calls (simulating the
// finish hook + sweeper firing together, plus duplicates) must start the next
// step EXACTLY ONCE — never two jobs at the same step_index.
func TestAdvanceWorkflowIdempotentConcurrent(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	// A 3-step chain where step 1 finishes fast; we then hammer advance manually.
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{echoStep("s1"), echoStep("s2"), echoStep("s3")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	// Wait for step 1's job to reach terminal WITHOUT letting the chain run away:
	// grab the step-1 job id and Wait on it. (The finish hook will also fire
	// advance, but that is part of what we are proving is safe.)
	waitStepJobTerminal(t, e, wf.ID, 1)

	// Hammer Advance concurrently (finish hook + sweeper + duplicates).
	const n = 24
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() { defer func() { done <- struct{}{} }(); e.Advance(wf.ID) }()
	}
	for i := 0; i < n; i++ {
		<-done
	}

	// After the dust settles the workflow runs to completion; crucially each
	// step_index has at most ONE job (no double-start).
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s (err=%s), want done", final.Status, final.Error)
	}
	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
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
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{echoStep("ok1"), failStep("boom2"), echoStep("never3")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow status = %s, want failed", final.Status)
	}
	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
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
	e := newTestEngine(t, t.TempDir())
	// Step 1 sleeps so the workflow is still on step 1 when we cancel.
	sleepStep := StepSpec{
		Name: "sleep1", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "10"}, Cwd: ".", TimeoutSec: 30,
	}
	wf, err := e.SubmitWorkflow(Spec{
		Steps: []StepSpec{sleepStep, echoStep("two"), echoStep("three")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	// Ensure step 1 is actually running before cancelling.
	waitStepJobRunning(t, e, wf.ID, 1)

	if err := e.CancelWorkflow(wf.ID); err != nil {
		t.Fatalf("CancelWorkflow: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowCancelled {
		t.Fatalf("workflow status = %s, want cancelled", final.Status)
	}
	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	for _, j := range jobs {
		if j.StepIndex >= 2 {
			t.Fatalf("step %d started after cancel (job %s)", j.StepIndex, j.ID)
		}
	}
	// Cancel is idempotent (second call is a no-op).
	if err := e.CancelWorkflow(wf.ID); err != nil {
		t.Fatalf("second CancelWorkflow: %v", err)
	}

	// Drain the cancelled step-1 job so its background goroutine finishes before
	// the test store is closed (best-effort recordEvent vs teardown).
	waitStepJobTerminal(t, e, wf.ID, 1)
}

// waitStepJobTerminal waits for the workflow's step-N job to reach terminal and
// returns it.
func waitStepJobTerminal(t *testing.T, e *Engine, wfID string, step int) jobstore.JobRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := e.meta.ListWorkflowJobs(wfID)
		if j := stepJob(jobs, step); j != nil && job.IsTerminal(j.Status) {
			return *j
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("step %d of %s did not reach terminal in time", step, wfID)
	return jobstore.JobRecord{}
}

// waitStepJobRunning waits for the workflow's step-N job to be running.
func waitStepJobRunning(t *testing.T, e *Engine, wfID string, step int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := e.meta.ListWorkflowJobs(wfID)
		if j := stepJob(jobs, step); j != nil && j.Status == job.StatusRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("step %d of %s did not start running in time", step, wfID)
}

// TestAdvanceRunningWorkflowsRecoversCrashedAdvance simulates a crash: a running
// workflow whose step-1 job reached done but whose finish-hook advance never ran
// (the next step was never started). The sweeper (AdvanceRunning) must
// re-drive it and start step 2.
func TestAdvanceRunningWorkflowsRecoversCrashedAdvance(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

	// Build the spec we want (2 echo steps) and persist a running workflow header
	// + a DONE step-1 job DIRECTLY via the store, bypassing the finish hook — this
	// is exactly the "advance was lost" state a crash leaves behind.
	spec := Spec{Steps: []StepSpec{echoStep("s1"), echoStep("s2")}}
	wfID := "wf-crash"
	specJSON := mustMarshalSpec(t, spec)
	if err := e.meta.InsertWorkflow(jobstore.Workflow{
		ID: wfID, Status: jobstore.WorkflowRunning, CurrentStep: 1, TotalSteps: 2,
		SpecJSON: specJSON, CallerID: "alice", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("InsertWorkflow: %v", err)
	}
	if err := e.meta.UpsertJob(jobstore.JobRecord{
		ID: "crash-step1", ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: job.StatusDone, ResultDir: "/tmp/x/crash-step1", StartedAt: 1,
		WorkflowID: wfID, StepIndex: 1,
	}); err != nil {
		t.Fatalf("UpsertJob step1: %v", err)
	}

	// Before the sweep: only step 1 exists.
	jobs, _ := e.meta.ListWorkflowJobs(wfID)
	if len(jobs) != 1 {
		t.Fatalf("pre-sweep step jobs = %d, want 1", len(jobs))
	}

	// Sweep: must recover and start step 2.
	n := e.AdvanceRunning(context.Background())
	if n != 1 {
		t.Fatalf("AdvanceRunning inspected %d, want 1 running", n)
	}

	final := waitWorkflow(t, e, wfID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s, want done after recovery", final.Status)
	}
	jobs, _ = e.meta.ListWorkflowJobs(wfID)
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
	e := newTestEngine(t, t.TempDir())
	if n := e.AdvanceRunning(context.Background()); n != 0 {
		t.Fatalf("sweep inspected %d, want 0 (no running workflows)", n)
	}
}

// mustMarshalSpec marshals a Spec exactly as SubmitWorkflow does.
func mustMarshalSpec(t *testing.T, spec Spec) string {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return string(b)
}

func requestFromJSON(t *testing.T, raw string) job.JobRequest {
	t.Helper()
	var req job.JobRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("request_json not valid JSON: %v\n%s", err, raw)
	}
	return req
}

func requestCmdContains(t *testing.T, raw, want string) bool {
	t.Helper()
	req := requestFromJSON(t, raw)
	return strings.Contains(strings.Join(req.Cmd, "\x00"), want)
}

// TestSubmitWorkflowRejectsInvalidStep asserts every step is validated at submit
// time: an invalid project/agent/runner in ANY step fails the whole submit before
// any job starts.
func TestSubmitWorkflowRejectsInvalidStep(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

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
			wf, err := e.SubmitWorkflow(Spec{Steps: tc.steps}, "alice")
			if err == nil {
				t.Fatalf("expected rejection, got workflow %s", wf.ID)
			}
		})
	}
}

// TestWorkflowResolvesRefsEndToEnd is the P2 integration proof (real exec): a 3-step
// chain where step1 writes result.json + emits a result_dir, step2's cmd references
// ${steps.1.result_dir}, and step3's prompt references ${steps.2.exit_code}. After
// the chain runs to done, each downstream step's PERSISTED request (request_json)
// must carry the substituted value — proving resolveRefs ran before each step's
// Submit and the real prior output flowed through.
func TestWorkflowResolvesRefsEndToEnd(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)

	// step1 writes a result.json so it has a real result_dir to hand downstream.
	step1 := StepSpec{
		Name: "gen", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", `printf '{"ok":true}' > "$GOFER_RESULT_DIR/result.json"`},
		Cwd: ".", TimeoutSec: 30,
	}
	// step2 consumes step1's result_dir as an argv element (path-by-reference).
	step2 := StepSpec{
		Name: "use-dir", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", `test -f "${steps.1.result_dir}/result.json"`},
		Cwd: ".", TimeoutSec: 30,
	}
	// step3 (exec) carries step2's exit_code in its prompt; the prompt is persisted
	// into request_json even though exec ignores it for execution — that is what we
	// assert (the substitution landed in the started step's request).
	step3 := StepSpec{
		Name: "use-code", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd:    []string{"true"},
		Prompt: "prior exit was ${steps.2.exit_code}",
		Cwd:    ".", TimeoutSec: 30,
	}

	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{step1, step2, step3}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s (err=%s), want done", final.Status, final.Error)
	}

	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("ran %d step jobs, want 3", len(jobs))
	}

	// step1's real result_dir must appear verbatim in step2's persisted request
	// (the ${steps.1.result_dir} ref was resolved to it before step2 started).
	step1Job := stepJob(jobs, 1)
	step2Job := stepJob(jobs, 2)
	step3Job := stepJob(jobs, 3)
	if step1Job == nil || step2Job == nil || step3Job == nil {
		t.Fatalf("missing a step job: %+v", jobs)
	}
	if step1Job.ResultDir == "" {
		t.Fatal("step1 has no result_dir")
	}
	if !requestCmdContains(t, step2Job.RequestJSON, step1Job.ResultDir) {
		t.Fatalf("step2 request does not contain step1 result_dir %q:\n%s", step1Job.ResultDir, step2Job.RequestJSON)
	}
	// step2 exited 0 (done), so step3's prompt must read "prior exit was 0".
	if !strings.Contains(step3Job.RequestJSON, "prior exit was 0") {
		t.Fatalf("step3 request missing substituted exit_code:\n%s", step3Job.RequestJSON)
	}
}

// TestWorkflowFailsWhenRefHasNoOutput is the runtime-missing-output proof: step2
// references ${steps.1.result} but step1 writes NO result.json. resolveRefs fails
// when advancing to step2, so the workflow goes failed with a clear error and step2
// never starts.
func TestWorkflowFailsWhenRefHasNoOutput(t *testing.T) {
	root := t.TempDir()
	e := newTestEngine(t, root)

	step1 := echoStep("no-result") // echoes, never writes result.json
	step2 := StepSpec{
		Name: "wants-result", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "echo ${steps.1.result}"},
		Cwd: ".", TimeoutSec: 30,
	}

	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{step1, step2}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow status = %s, want failed (step1 wrote no result.json)", final.Status)
	}
	if !strings.Contains(final.Error, "resolve refs") {
		t.Fatalf("workflow error should mention resolve refs, got: %q", final.Error)
	}

	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	// step2 must NOT have started (its refs could not be resolved).
	if stepJob(jobs, 2) != nil {
		t.Fatal("step2 started despite unresolvable ref")
	}
}
