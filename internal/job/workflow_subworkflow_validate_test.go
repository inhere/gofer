package job

import (
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestSubmitWorkflowSubValidation drives the P3 submit-time准入 table for nesting:
// type/sub_workflow coupling, recursive single-job admission, depth limit, and the
// fan-out × workflow mutex. Each invalid case must be rejected at submit (no DB row).
func TestSubmitWorkflowSubValidation(t *testing.T) {
	s := newTestService(t, t.TempDir())

	good := echoStep("ok")
	// A legal 1-level sub-workflow step.
	legalSub := wfStep("sub", "", echoStep("c1"), echoStep("c2"))

	// A workflow-type step whose sub-workflow has an ILLEGAL leaf (exec on noexec project)
	// — recursive admission must reject it (nesting does not bypass the exec gate).
	illegalLeaf := wfStep("sub-bad", "",
		StepSpec{Name: "bad", ProjectKey: "noexec", Agent: "exec", Runner: "local", Cmd: []string{"true"}},
	)

	// A workflow-type step whose sub-workflow has an unknown project leaf.
	unknownProjLeaf := wfStep("sub-ghost", "",
		StepSpec{Name: "bad", ProjectKey: "ghost", Agent: "exec", Runner: "local", Cmd: []string{"true"}},
	)

	// type=workflow with NO sub_workflow.
	noSub := StepSpec{Name: "nosub", Type: stepTypeWorkflow}

	// type=workflow with an EMPTY sub_workflow.
	emptySub := StepSpec{Name: "empty", Type: stepTypeWorkflow, SubWorkflow: &WorkflowSpec{}}

	// type=job that wrongly carries a sub_workflow.
	jobWithSub := StepSpec{
		Name: "jws", ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"true"},
		Type: stepTypeJob, SubWorkflow: &WorkflowSpec{Steps: []StepSpec{echoStep("x")}},
	}

	// unknown type.
	badType := StepSpec{Name: "bt", Type: "loop", ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"true"}}

	// fan-out × workflow: a workflow-type step with fan_out>1 (mutually exclusive).
	fanWf := func() StepSpec {
		st := wfStep("fanwf", "", echoStep("c1"))
		st.FanOut = 3
		return st
	}()

	// depth>3: top(1) -> sub(2) -> sub(3) -> sub(4) exceeds maxWorkflowDepth=3.
	depth4 := wfStep("d2", "", // depth 2
		wfStep("d3", "", // depth 3
			wfStep("d4", "", echoStep("leaf")), // depth 4 — too deep
		),
	)

	// depth==3 exactly: top(1)->sub(2)->sub(3) is the maximum allowed (legal).
	depth3 := wfStep("d2", "", // depth 2
		wfStep("d3", "", echoStep("leaf")), // depth 3 — legal
	)

	cases := []struct {
		name    string
		steps   []StepSpec
		wantErr bool
	}{
		{"legal nested", []StepSpec{good, legalSub}, false},
		{"legal depth 3", []StepSpec{depth3}, false},
		{"type=workflow no sub_workflow", []StepSpec{noSub}, true},
		{"type=workflow empty sub_workflow", []StepSpec{emptySub}, true},
		{"type=job with sub_workflow", []StepSpec{jobWithSub}, true},
		{"unknown type", []StepSpec{badType}, true},
		{"nested illegal exec leaf", []StepSpec{illegalLeaf}, true},
		{"nested unknown project leaf", []StepSpec{unknownProjLeaf}, true},
		{"fan-out x workflow mutex", []StepSpec{fanWf}, true},
		{"depth over limit", []StepSpec{depth4}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, err := s.SubmitWorkflow(WorkflowSpec{Steps: tc.steps}, "alice")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected rejection, got workflow %s", wf.ID)
				}
				// Submit-time validation maps to ErrInvalidRequest (400) or ErrUnknownProject (404).
				if !errors.Is(err, ErrInvalidRequest) && !errors.Is(err, ErrUnknownProject) {
					t.Fatalf("error %v is neither ErrInvalidRequest nor ErrUnknownProject", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
			// A legal submit must NOT have started the workflow into a broken state; cancel
			// it so any started inner jobs are torn down before teardown.
			_ = s.CancelWorkflow(wf.ID)
			// Drain to terminal to avoid background goroutines racing store close.
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				w, _, _ := s.meta.GetWorkflow(wf.ID)
				if w.Status != jobstore.WorkflowRunning {
					break
				}
				time.Sleep(15 * time.Millisecond)
			}
		})
	}
}

// TestValidateSubworkflowRecursiveDepth is a focused unit check on validateSubworkflow's
// depth accounting (independent of submit): depth 3 passes, depth 4 fails.
func TestValidateSubworkflowRecursiveDepth(t *testing.T) {
	s := newTestService(t, t.TempDir())
	cfg := s.config()

	d3 := WorkflowSpec{Steps: []StepSpec{
		wfStep("d2", "", wfStep("d3", "", echoStep("leaf"))),
	}}
	if err := s.validateSubworkflow(d3, cfg, 1); err != nil {
		t.Fatalf("depth 3 should pass, got: %v", err)
	}

	d4 := WorkflowSpec{Steps: []StepSpec{
		wfStep("d2", "", wfStep("d3", "", wfStep("d4", "", echoStep("leaf")))),
	}}
	if err := s.validateSubworkflow(d4, cfg, 1); err == nil {
		t.Fatal("depth 4 should be rejected (exceeds maxWorkflowDepth)")
	}
}
