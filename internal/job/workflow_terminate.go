package job

import (
	"github.com/inhere/gofer/internal/jobstore"
)

// setWorkflowDone marks a workflow done and records the terminal event. The caller
// has already won the AdvanceStep for the final step, so this runs exactly once.
//
// The terminal event is recorded BEFORE the status flip (mirroring finish's job
// terminal ordering): a watcher polling for status!=running could otherwise observe
// done and read the event log BEFORE this terminal row lands, missing the terminal
// frame. Recording first reflects the already-decided outcome and closes that race.
func (s *Service) setWorkflowDone(wfID string) {
	s.recordWorkflowEvent(wfID, EventWorkflowTerminal, map[string]any{
		"status": jobstore.WorkflowDone,
	})
	_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowDone, "")
	// P4/T4.3: count the terminal + observe the whole-chain duration (nil-safe).
	s.recordWorkflowTerminalMetric(wfID, jobstore.WorkflowDone)
	// P3: if this is a sub-workflow, its terminal transition unlocks the parent step.
	s.triggerParentAdvance(wfID)
}

// recordWorkflowTerminalMetric counts one workflow terminal + observes its
// submit→terminal duration through the MetricsSink (P4/T4.3, design §9). It is
// nil-safe and BEST-EFFORT (a store read failure only skips the duration sample, never
// affects the terminal transition). Duration is now−created_at, clamped at 0 against
// clock skew. Called from setWorkflowDone/setWorkflowFailed (the AdvanceStep winner, so
// it runs once per terminal) and the cancel path.
func (s *Service) recordWorkflowTerminalMetric(wfID, status string) {
	if s.metrics == nil {
		return
	}
	dur := 0.0
	if wf, ok, err := s.meta.GetWorkflow(wfID); err == nil && ok {
		dur = float64(s.nowFn().Unix() - wf.CreatedAt)
		if dur < 0 {
			dur = 0
		}
	}
	s.metrics.WorkflowTerminal(status, dur)
}

// triggerParentAdvance fires the parent's advanceWorkflow when wfID is a sub-workflow
// (ParentWorkflowID != "") that just reached a terminal state (P3, D19). It mirrors the
// finish hook's `go advanceWorkflow`: ASYNC + 幂等 (the parent's AdvanceStep抢权 + the
// deterministic child id keep a racing trigger and the sweeper's backstop safe). A
// top-level workflow (no parent) is a no-op. Best-effort: a store read error or a
// missing parent only skips the prompt re-drive — the sweeper still re-drives the
// running parent on its next tick (子 wf 终态但父 advance 漏触发的兜底).
func (s *Service) triggerParentAdvance(wfID string) {
	wf, ok, err := s.meta.GetWorkflow(wfID)
	if err != nil || !ok || wf.ParentWorkflowID == "" {
		return
	}
	go s.advanceWorkflow(wf.ParentWorkflowID)
}

// setWorkflowFailed marks a workflow failed with a reason and records the terminal
// event. The caller has already won the AdvanceStep (or is on the submit-source
// path), so this runs once per workflow. The terminal event is recorded BEFORE the
// status flip (see setWorkflowDone — closes the watcher-races-terminal-event gap).
func (s *Service) setWorkflowFailed(wfID, reason string) {
	s.recordWorkflowEvent(wfID, EventWorkflowTerminal, map[string]any{
		"status": jobstore.WorkflowFailed, "error": reason,
	})
	_ = s.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, reason)
	// P4/T4.3: count the terminal + observe the whole-chain duration (nil-safe).
	s.recordWorkflowTerminalMetric(wfID, jobstore.WorkflowFailed)
	// P3: if this is a sub-workflow, its terminal transition unlocks the parent step
	// (which then sees a failed child → step failed → on_failure).
	s.triggerParentAdvance(wfID)
}
