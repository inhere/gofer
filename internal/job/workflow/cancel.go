package workflow

import (
	"fmt"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// CancelWorkflow stops a running workflow: it marks it cancelled (so Advance
// never starts another step) and cancels the currently-running step's job. It is
// idempotent and a no-op for an unknown or already-terminal workflow.
//
// Order matters: set cancelled FIRST so any racing Advance (which checks
// status==running) bails, THEN cancel the active step's job. The cancelled step
// reaching terminal will fire Advance, but the running-status guard there
// stops it from starting the next step.
func (e *Engine) CancelWorkflow(wfID string) error {
	wf, ok, err := e.meta.GetWorkflow(wfID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown workflow %q", wfID)
	}
	if wf.Status != jobstore.WorkflowRunning {
		return nil // already terminal: idempotent no-op
	}

	if err := e.meta.SetWorkflowStatus(wfID, jobstore.WorkflowCancelled, ""); err != nil {
		return err
	}
	e.recordWorkflowEvent(wfID, job.EventWorkflowCancelled, map[string]any{
		"step": wf.CurrentStep, "attempt": wf.StepAttempt,
	})
	// P4/T4.3: count the cancelled terminal + observe the duration (nil-safe). We have
	// the header in hand (wf) so compute the duration from its created_at directly.
	if e.metrics != nil {
		dur := float64(e.now().Unix() - wf.CreatedAt)
		if dur < 0 {
			dur = 0
		}
		e.metrics.WorkflowTerminal(jobstore.WorkflowCancelled, dur)
	}

	// Cancel the active (step, attempt) generation's job(s) (Cancel is a stable no-op
	// for a terminal job). Match the current attempt so a retried step cancels the live
	// run; for a fan-out step this cancels EVERY in-flight fan of the generation (P2).
	jobs, err := e.meta.ListWorkflowJobs(wfID)
	if err != nil {
		// P3: even on the best-effort job-cancel error path, a cancelled sub-workflow must
		// still unlock its parent step (parent sees cancelled → failed → on_failure).
		e.triggerParentAdvance(wfID)
		return nil // status is already cancelled; job-cancel is best-effort
	}
	for _, j := range stepFanJobs(jobs, wf.CurrentStep, wf.StepAttempt) {
		_ = e.ops.Cancel(j.ID)
	}
	// P3: a cancelled sub-workflow unlocks its parent step (parent sees cancelled →
	// step failed → on_failure). A top-level workflow is a no-op (no parent).
	e.triggerParentAdvance(wfID)
	return nil
}
