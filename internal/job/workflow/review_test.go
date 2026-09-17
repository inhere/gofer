package workflow

import (
	"testing"
	"time"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// acceptOps is the review half of the host job service the workflow engine itself
// never calls: the engine only OPENS the review gate (StepSpec.Review), while the
// human accept/reject arrives out-of-band (HTTP/CLI/web). The tests reach it by type
// assertion so workflow.JobOps stays the narrow capability surface (layering §13.3).
type acceptOps interface {
	AcceptJob(jobID, by, note string) (job.ReviewOutcome, error)
	RejectJob(jobID, by, note string, resume bool) (job.ReviewOutcome, error)
}

// stepJobID returns the (only) step-job id of a running workflow's current step.
func stepJobID(t *testing.T, e *Engine, wfID string) string {
	t.Helper()
	jobs, err := e.meta.ListWorkflowJobs(wfID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("workflow %s has %d step jobs, want 1", wfID, len(jobs))
	}
	return jobs[0].ID
}

// waitStepStatus polls until the workflow's only step-job reaches want.
func waitStepStatus(t *testing.T, e *Engine, wfID, want string) jobstore.JobRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := e.meta.ListWorkflowJobs(wfID)
		if err != nil {
			t.Fatalf("ListWorkflowJobs: %v", err)
		}
		if len(jobs) == 1 && jobs[0].Status == want {
			return jobs[0]
		}
		time.Sleep(15 * time.Millisecond)
	}
	jobs, _ := e.meta.ListWorkflowJobs(wfID)
	t.Fatalf("step job never reached %s (jobs=%v)", want, jobs)
	return jobstore.JobRecord{}
}

// reviewStep builds a step that must be accepted by a human before the chain moves on.
func reviewStep(name string, review bool) StepSpec {
	st := echoStep(name)
	st.Review = &review
	return st
}

// TestWorkflowWaitsForReviewThenAdvances: a step with review:true parks the chain
// while its job is needs_review (非终态 → the aggregation must NOT decide), and the
// chain advances only once a human accepts.
func TestWorkflowWaitsForReviewThenAdvances(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Title: "reviewed chain",
		Steps: []StepSpec{reviewStep("a", true), echoStep("b")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	step := waitStepStatus(t, e, wf.ID, job.StatusNeedsReview)
	if !step.RequireReview {
		t.Fatalf("step job must record require_review: %+v", step)
	}
	// The chain has NOT advanced: the review is the step's completion, not its launch.
	time.Sleep(200 * time.Millisecond)
	cur, ok, err := e.meta.GetWorkflow(wf.ID)
	if err != nil || !ok {
		t.Fatalf("GetWorkflow ok=%v err=%v", ok, err)
	}
	if cur.Status != jobstore.WorkflowRunning || cur.CurrentStep != 1 {
		t.Fatalf("workflow = %s step %d, want running/1 while the step awaits review", cur.Status, cur.CurrentStep)
	}
	if jobs, _ := e.meta.ListWorkflowJobs(wf.ID); len(jobs) != 1 {
		t.Fatalf("step 2 must not start before the review, got %d jobs", len(jobs))
	}

	acc := e.ops.(acceptOps)
	if _, err := acc.AcceptJob(step.ID, "alice", "ok"); err != nil {
		t.Fatalf("AcceptJob: %v", err)
	}

	// The accept drives the SAME advance hook a done step job would.
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow = %s, want done", final.Status)
	}
	jobs, err := e.meta.ListWorkflowJobs(wf.ID)
	if err != nil {
		t.Fatalf("ListWorkflowJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("chain has %d jobs, want 2 (step 2 ran after the accept)", len(jobs))
	}
}

// TestWorkflowRejectFailsStep: a rejected step job is terminal-but-not-done, so the
// chain aggregates it as a failure (fail-fast by default) instead of hanging.
func TestWorkflowRejectFailsStep(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{
		Title: "rejected chain",
		Steps: []StepSpec{reviewStep("a", true), echoStep("b")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	step := waitStepStatus(t, e, wf.ID, job.StatusNeedsReview)
	acc := e.ops.(acceptOps)
	if _, err := acc.RejectJob(step.ID, "bob", "not what I asked for", false); err != nil {
		t.Fatalf("RejectJob: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("workflow = %s, want failed (a rejected step is a failed step)", final.Status)
	}
	if jobs, _ := e.meta.ListWorkflowJobs(wf.ID); len(jobs) != 1 {
		t.Fatalf("a failed step must not start the next one, got %d jobs", len(jobs))
	}
}

// TestStepReviewOverridesProjectDefault: an explicit step-level review:false beats the
// project's require_review default — the step's job finishes done and the chain runs
// straight through.
func TestStepReviewOverridesProjectDefault(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	cfg := e.ops.Config()
	proj := cfg.Projects["self"]
	proj.RequireReview = true
	cfg.Projects["self"] = proj

	wf, err := e.SubmitWorkflow(Spec{
		Title: "override",
		Steps: []StepSpec{reviewStep("a", false)},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow = %s, want done (step review:false overrides require_review)", final.Status)
	}
}
