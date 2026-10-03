package workflow

import (
	"encoding/json"
	"fmt"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// PickWorkflowFan records the human choice for a join=pick step and re-drives the
// normal workflow advance path. The chosen fan must already be terminal; no merge
// or push is performed here.
func (e *Engine) PickWorkflowFan(wfID string, stepIndex, fanIndex int) (jobstore.Workflow, error) {
	wf, ok, err := e.meta.GetWorkflow(wfID)
	if err != nil {
		return jobstore.Workflow{}, err
	}
	if !ok {
		return jobstore.Workflow{}, fmt.Errorf("workflow %q not found", wfID)
	}
	if wf.Status != jobstore.WorkflowRunning {
		return wf, fmt.Errorf("workflow %q is not running", wfID)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(wf.SpecJSON), &spec); err != nil {
		return wf, fmt.Errorf("decode workflow spec: %w", err)
	}
	if stepIndex != wf.CurrentStep || stepIndex < 1 || stepIndex > len(spec.Steps) {
		return wf, fmt.Errorf("workflow current step is %d, want %d", wf.CurrentStep, stepIndex)
	}
	step := spec.Steps[stepIndex-1]
	if joinPolicy(step) != joinPick {
		return wf, fmt.Errorf("step %d does not use join=pick", stepIndex)
	}
	if fanIndex < 1 || fanIndex > fanWant(step) {
		return wf, fmt.Errorf("fan index %d is outside 1..%d", fanIndex, fanWant(step))
	}
	jobs, err := e.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return wf, err
	}
	fans := stepFanJobs(jobs, stepIndex, wf.StepAttempt)
	var picked *jobstore.JobRecord
	for _, fan := range fans {
		if fan.FanIndex == fanIndex {
			picked = fan
			break
		}
	}
	if picked == nil || !job.IsTerminal(picked.Status) {
		return wf, fmt.Errorf("fan %d is not terminal", fanIndex)
	}
	if spec.Picked == nil {
		spec.Picked = map[int]int{}
	}
	spec.Picked[stepIndex] = fanIndex
	b, err := json.Marshal(spec)
	if err != nil {
		return wf, fmt.Errorf("marshal picked workflow spec: %w", err)
	}
	if err := e.meta.UpdateWorkflowSpec(wfID, string(b)); err != nil {
		return wf, err
	}
	e.recordWorkflowEvent(wfID, job.EventStepPicked, map[string]any{"step": stepIndex, "fan": fanIndex})
	e.Advance(wfID)
	got, _, _ := e.meta.GetWorkflow(wfID)
	return got, nil
}
