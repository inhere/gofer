package workflow

import "testing"

// TestStepRequestsCarryTheWorkflowCreatorAsCeilingOwner: every job a workflow step starts
// (single, fan-out, at submit-time validation and at advance time) is bounded by the ceiling
// of the caller that created the workflow.
func TestStepRequestsCarryTheWorkflowCreatorAsCeilingOwner(t *testing.T) {
	t.Parallel()
	step := StepSpec{Name: "a", ProjectKey: "p1", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}}
	if got := stepToRequest(step, "wf-1", 1, 1, 0, "ci-bot").OriginCaller; got != "ci-bot" {
		t.Errorf("single step OriginCaller = %q, want ci-bot", got)
	}
	if got := stepToRequestForFan(step, "wf-1", 1, 1, 2, "ci-bot").OriginCaller; got != "ci-bot" {
		t.Errorf("fan step OriginCaller = %q, want ci-bot", got)
	}
}
