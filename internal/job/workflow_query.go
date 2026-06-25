package job

import (
	"encoding/json"
	"log"
	"sort"

	"github.com/inhere/gofer/internal/jobstore"
)

// GetWorkflow returns a workflow header by id (HTTP detail/cancel paths). The
// bool is false when no such workflow exists. It is a thin pass-through to the
// metadata store so httpapi never reaches into the unexported store.
func (s *Service) GetWorkflow(id string) (jobstore.Workflow, bool, error) {
	return s.meta.GetWorkflow(id)
}

// ListWorkflows returns workflow headers, optionally filtered by status, newest
// first, capped at limit (<=0 => store default). HTTP list path.
func (s *Service) ListWorkflows(status string, limit int) ([]jobstore.Workflow, error) {
	return s.meta.ListWorkflows(status, limit)
}

// WorkflowSteps returns the per-step summary for a workflow's detail view, in
// step order. It reads the started step-jobs (a step not yet reached has no job
// row, so the list only contains started steps — the chain is strictly serial).
// The name is recovered from the step-job's persisted request (Title == step
// name).
func (s *Service) WorkflowSteps(wfID string) ([]WorkflowStep, error) {
	jobs, err := s.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowStep, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, WorkflowStep{
			StepIndex: j.StepIndex,
			Attempt:   j.Attempt,
			FanIndex:  j.FanIndex,
			Name:      TitleFromRequestJSON(j.RequestJSON),
			JobID:     j.ID,
			Status:    j.Status,
		})
	}
	// P3 UI fix: workflow-type steps run NO step-job, so they are missing from the
	// job-derived rows above (the web/CLI chain only saw job steps, hiding the whole
	// sub-workflow). Surface each such step from the spec + its child workflow so the
	// chain shows the step and can link into the child's detail.
	if wf, ok, gerr := s.meta.GetWorkflow(wfID); gerr == nil && ok {
		var spec WorkflowSpec
		if json.Unmarshal([]byte(wf.SpecJSON), &spec) == nil {
			for i := range spec.Steps {
				if spec.Steps[i].Type != stepTypeWorkflow {
					continue
				}
				row := WorkflowStep{StepIndex: i + 1, Name: spec.Steps[i].Name, Type: stepTypeWorkflow}
				// The child may not exist yet (step not reached) — then the row is a
				// pending placeholder; once started, link + status come from the child.
				if child, found, cerr := s.meta.FindChildWorkflow(wfID, i+1); cerr == nil && found {
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
func (s *Service) recordWorkflowEvent(wfID, eventType string, detail any) {
	var dj string
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil && len(b) <= MaxEventDetailBytes {
			dj = string(b)
		}
	}
	if _, err := s.meta.InsertWorkflowEvent(jobstore.WorkflowEvent{
		WorkflowID: wfID,
		Type:       eventType,
		Detail:     dj,
		At:         s.nowFn().Unix(),
	}); err != nil {
		log.Printf("recordWorkflowEvent: workflow %s type %s: %v", wfID, eventType, err)
	}
}

// ListWorkflowEvents returns a workflow's append-only lifecycle events in seq order
// (P1), forwarding to the metadata store. sinceSeq > 0 returns only events after
// that cursor (the HTTP ?since incremental path).
func (s *Service) ListWorkflowEvents(wfID string, sinceSeq int64) ([]jobstore.WorkflowEvent, error) {
	return s.meta.ListWorkflowEvents(wfID, sinceSeq)
}
