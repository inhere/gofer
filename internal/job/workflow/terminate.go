package workflow

import (
	"encoding/json"
	"log/slog"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// recordWorkflowTerminalMetric counts one workflow terminal + observes its
// submit→terminal duration through the job.MetricsSink (P4/T4.3, design §9). It is
// nil-safe and BEST-EFFORT (a store read failure only skips the duration sample, never
// affects the terminal transition). Duration is now−created_at, clamped at 0 against
// clock skew. Called from finishStep/setWorkflowFailed (the claim winner, so
// it runs once per terminal) and the cancel path.
func (e *Engine) recordWorkflowTerminalMetric(wfID, status string) {
	if e.metrics == nil {
		return
	}
	dur := 0.0
	if wf, ok, err := e.meta.GetWorkflow(wfID); err == nil && ok {
		dur = float64(e.now().Unix() - wf.CreatedAt)
		if dur < 0 {
			dur = 0
		}
	}
	e.metrics.WorkflowTerminal(status, dur)
}

// triggerParentAdvance fires the parent's Advance when wfID is a sub-workflow
// (ParentWorkflowID != "") that just reached a terminal state (P3, D19). It mirrors the
// finish hook's `go Advance`: ASYNC + 幂等 (the parent's AdvanceStep抢权 + the
// deterministic child id keep a racing trigger and the sweeper's backstop safe). A
// top-level workflow (no parent) is a no-op. Best-effort: a store read error or a
// missing parent only skips the prompt re-drive — the sweeper still re-drives the
// running parent on its next tick (子 wf 终态但父 advance 漏触发的兜底).
func (e *Engine) triggerParentAdvance(wfID string) {
	wf, ok, err := e.meta.GetWorkflow(wfID)
	if err != nil || !ok || wf.ParentWorkflowID == "" {
		return
	}
	base, parent := e.baseEngine(), wf.ParentWorkflowID
	base.goAsync(func() { base.Advance(parent) })
}

// setWorkflowFailed marks a workflow failed with a reason and records the terminal
// event. The caller has already won the AdvanceStep (or is on the submit-source
// path), so this runs once per workflow. The terminal event is recorded BEFORE the
// status flip (a watcher polling for status!=running must not observe the terminal
// status before the terminal event row lands).
func (e *Engine) setWorkflowFailed(wfID, reason string) {
	e.recordWorkflowEvent(wfID, job.EventWorkflowTerminal, map[string]any{
		"status": jobstore.WorkflowFailed, "error": reason,
	})
	// best-effort：失败不阻断终态推进，但记 warning，否则 workflow 头部状态与实际静默漂移。
	if err := e.meta.SetWorkflowStatus(wfID, jobstore.WorkflowFailed, reason); err != nil {
		slog.Warn("set workflow failed", "workflow_id", wfID, "reason", reason, "err", err)
	}
	// P4/T4.3: count the terminal + observe the whole-chain duration (nil-safe).
	e.recordWorkflowTerminalMetric(wfID, jobstore.WorkflowFailed)
	// P3: if this is a sub-workflow, its terminal transition unlocks the parent step
	// (which then sees a failed child → step failed → on_failure).
	e.triggerParentAdvance(wfID)
}

// finishStep claims the (cur, att) generation of a running workflow and moves it to
// a terminal status in ONE store transaction (jobstore.FinishWorkflowStep), then runs
// the winner-only side effects (terminal metric, parent re-drive). It returns whether
// this caller won. Use it instead of "AdvanceStep to cur+1, then setWorkflowDone/
// Failed": that pair left the workflow readable as running at a step with no jobs,
// so a concurrent Advance could start the next step after a fail-fast, or see the
// pointer past the last step and fail a successful workflow.
// pre are events recorded (in order) before the terminal event in the same
// transaction, e.g. step.skipped for an on_failure=continue last step.
func (e *Engine) finishStep(wfID string, cur, att int, status, reason string, pre ...jobstore.WorkflowEvent) bool {
	detail := map[string]any{"status": status}
	if reason != "" {
		detail["error"] = reason
	}
	var dj string
	if b, err := json.Marshal(detail); err == nil && len(b) <= job.MaxEventDetailBytes {
		dj = string(b)
	}
	ev := jobstore.WorkflowEvent{WorkflowID: wfID, Type: job.EventWorkflowTerminal, Detail: dj, At: e.now().Unix()}
	won, err := e.meta.FinishWorkflowStep(wfID, cur, att, cur+1, status, reason, append(pre, ev)...)
	if err != nil {
		slog.Warn("finish workflow step", "workflow_id", wfID, "step", cur, "attempt", att, "status", status, "err", err)
		return false
	}
	if !won {
		return false
	}
	e.recordWorkflowTerminalMetric(wfID, status)
	e.triggerParentAdvance(wfID)
	return true
}

// workflowEvent builds an event row the way recordWorkflowEvent does (detail JSON,
// dropped when over MaxEventDetailBytes), for callers that write it inside a store
// transaction instead of appending it on its own.
func (e *Engine) workflowEvent(wfID, eventType string, detail any) jobstore.WorkflowEvent {
	var dj string
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil && len(b) <= job.MaxEventDetailBytes {
			dj = string(b)
		}
	}
	return jobstore.WorkflowEvent{WorkflowID: wfID, Type: eventType, Detail: dj, At: e.now().Unix()}
}
