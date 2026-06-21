package job

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

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
