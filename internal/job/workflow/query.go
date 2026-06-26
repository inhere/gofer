package workflow

import (
	"encoding/json"
	"log/slog"
	"sort"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// GetWorkflow returns a workflow header by id (HTTP detail/cancel paths). The
// bool is false when no such workflow exists. It is a thin pass-through to the
// metadata store so httpapi never reaches into the unexported store.
func (e *Engine) GetWorkflow(id string) (jobstore.Workflow, bool, error) {
	return e.meta.GetWorkflow(id)
}

// ListWorkflows returns workflow headers, optionally filtered by status, newest
// first, capped at limit (<=0 => store default). HTTP list path.
func (e *Engine) ListWorkflows(status string, limit int) ([]jobstore.Workflow, error) {
	return e.meta.ListWorkflows(status, limit)
}

// WorkflowSteps returns the per-step summary for a workflow's detail view, in
// step order. It reads the started step-jobs (a step not yet reached has no job
// row, so the list only contains started steps — the chain is strictly serial).
// The name is recovered from the step-job's persisted request (Title == step
// name).
func (e *Engine) WorkflowSteps(wfID string) ([]Step, error) {
	jobs, err := e.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return nil, err
	}
	out := make([]Step, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, Step{
			StepIndex: j.StepIndex,
			Attempt:   j.Attempt,
			FanIndex:  j.FanIndex,
			Name:      job.TitleFromRequestJSON(j.RequestJSON),
			JobID:     j.ID,
			Status:    j.Status,
		})
	}
	// P3 UI fix: workflow-type steps run NO step-job, so they are missing from the
	// job-derived rows above (the web/CLI chain only saw job steps, hiding the whole
	// sub-workflow). Surface each such step from the spec + its child workflow so the
	// chain shows the step and can link into the child's detail.
	if wf, ok, gerr := e.meta.GetWorkflow(wfID); gerr == nil && ok {
		var spec Spec
		if json.Unmarshal([]byte(wf.SpecJSON), &spec) == nil {
			for i := range spec.Steps {
				if spec.Steps[i].Type != stepTypeWorkflow {
					continue
				}
				row := Step{StepIndex: i + 1, Name: spec.Steps[i].Name, Type: stepTypeWorkflow}
				// The child may not exist yet (step not reached) — then the row is a
				// pending placeholder; once started, link + status come from the child.
				if child, found, cerr := e.meta.FindChildWorkflow(wfID, i+1); cerr == nil && found {
					row.ChildWorkflowID = child.ID
					row.Status = child.Status
				}
				out = append(out, row)
			}
		}
	}
	// Merge the appended workflow rows into step order (job rows are already
	// step-ordered; stable keeps fan/attempt order within a step intact).
	sort.SliceStable(out, func(a, b int) bool { return out[a].StepIndex < out[b].StepIndex })
	return out, nil
}

// recordWorkflowEvent appends one append-only workflow lifecycle event (P1, design
// §5.4). It mirrors recordEvent (job_events): BEST-EFFORT — a marshal failure, an
// oversized detail or a write error only logs a warning and MUST NOT affect the
// workflow's推进/terminal state. detail must not carry secrets (SR403).
func (e *Engine) recordWorkflowEvent(wfID, eventType string, detail any) {
	var dj string
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil && len(b) <= job.MaxEventDetailBytes {
			dj = string(b)
		}
	}
	if _, err := e.meta.InsertWorkflowEvent(jobstore.WorkflowEvent{
		WorkflowID: wfID,
		Type:       eventType,
		Detail:     dj,
		At:         e.now().Unix(),
	}); err != nil {
		slog.Warn("recordWorkflowEvent: insert workflow event", "workflow_id", wfID, "type", eventType, "err", err)
	}
}

// ListWorkflowEvents returns a workflow's append-only lifecycle events in seq order
// (P1), forwarding to the metadata store. sinceSeq > 0 returns only events after
// that cursor (the HTTP ?since incremental path).
func (e *Engine) ListWorkflowEvents(wfID string, sinceSeq int64) ([]jobstore.WorkflowEvent, error) {
	return e.meta.ListWorkflowEvents(wfID, sinceSeq)
}
