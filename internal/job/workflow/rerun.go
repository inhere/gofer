package workflow

import (
	"encoding/json"
	"fmt"
	"strconv"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Rerun modes accepted by `wf rerun` / POST /v1/workflows/{id}/rerun (design §一·7).
const (
	// RerunModeFailed keeps only the FAILED steps of the original spec (and, inside a
	// failed fan-out step, only the fans that failed).
	RerunModeFailed = "failed"
	// RerunModeFrom keeps the named step and everything after it.
	RerunModeFrom = "from"
)

// RerunWorkflow submits a NEW workflow trimmed out of an existing one's spec (design
// §一·7): mode RerunModeFailed keeps the failed steps (a failed fan-out step keeps only
// its failed fans), RerunModeFrom keeps step `step` and every later step. The new
// workflow records the original as rerun_of; the original is never modified (its
// status, pointer and jobs stay as they are), so its history remains readable.
//
// The trimmed spec is re-submitted through the normal path, so it passes the same
// admission, per-step 入 and reference checks: a retained step whose ${steps.N.…}
// reference pointed at a dropped step is rejected rather than run with a broken
// reference. Variables and `ref:` shas are whatever the stored spec holds (already
// rendered and resolved at the original submit), so a rerun checks the same commit.
//
// The bool is false when no such workflow exists.
func (e *Engine) RerunWorkflow(wfID, mode, step, callerID string) (jobstore.Workflow, bool, error) {
	view, release, err := e.admitted()
	if err != nil {
		return jobstore.Workflow{}, true, err
	}
	defer release()
	return view.rerunImpl(wfID, mode, step, callerID)
}

func (e *Engine) rerunImpl(wfID, mode, step, callerID string) (jobstore.Workflow, bool, error) {
	if mode != RerunModeFailed && mode != RerunModeFrom {
		return jobstore.Workflow{}, true, fmt.Errorf("%w: rerun mode must be %q or %q", job.ErrInvalidRequest, RerunModeFailed, RerunModeFrom)
	}
	if mode == RerunModeFrom && step == "" {
		return jobstore.Workflow{}, true, fmt.Errorf("%w: rerun --from needs a step (number or name)", job.ErrInvalidRequest)
	}
	wf, ok, err := e.meta.GetWorkflow(wfID)
	if err != nil || !ok {
		return jobstore.Workflow{}, ok, err
	}
	if wf.Status == jobstore.WorkflowRunning {
		return jobstore.Workflow{}, true, fmt.Errorf("%w: workflow %s is still running (cancel it first)", job.ErrInvalidRequest, wfID)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(wf.SpecJSON), &spec); err != nil {
		return jobstore.Workflow{}, true, fmt.Errorf("decode workflow %s spec: %w", wfID, err)
	}
	jobs, err := e.meta.ListWorkflowJobs(wfID)
	if err != nil {
		return jobstore.Workflow{}, true, err
	}
	var trimmed Spec
	switch mode {
	case RerunModeFailed:
		trimmed = trimFailedSteps(spec, e.failedSteps(wfID, spec, jobs))
	case RerunModeFrom:
		idx, ierr := rerunStepIndex(spec, step)
		if ierr != nil {
			return jobstore.Workflow{}, true, ierr
		}
		trimmed = trimFromStep(spec, idx)
	}
	if len(trimmed.Steps) == 0 {
		return jobstore.Workflow{}, true, fmt.Errorf("%w: nothing to rerun (the original workflow has no failed step)", job.ErrInvalidRequest)
	}
	// A sub-workflow of a parent step is re-created by its parent, never re-run on its
	// own: the parent's step would still be bound to the old child id.
	if wf.ParentWorkflowID != "" {
		return jobstore.Workflow{}, true, fmt.Errorf("%w: workflow %s is a sub-workflow (rerun its parent)", job.ErrInvalidRequest, wfID)
	}
	nw, err := e.submitWorkflowImpl(trimmed, callerID, "", 0, 0, wfID)
	if err != nil {
		return jobstore.Workflow{}, true, err
	}
	e.recordWorkflowEvent(nw.ID, job.EventWorkflowRerun, map[string]any{
		"rerun_of": wfID, "mode": mode, "step": step, "steps": len(trimmed.Steps),
	})
	return nw, true, nil
}

// rerunStepIndex resolves a `--from` selector: a 1-based step number or a step name
// (the first step carrying that name).
func rerunStepIndex(spec Spec, sel string) (int, error) {
	if n, err := strconv.Atoi(sel); err == nil {
		if n < 1 || n > len(spec.Steps) {
			return 0, fmt.Errorf("%w: step %d is out of range (the workflow has %d step(s))", job.ErrInvalidRequest, n, len(spec.Steps))
		}
		return n, nil
	}
	for i := range spec.Steps {
		if spec.Steps[i].Name == sel {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("%w: no step named %q", job.ErrInvalidRequest, sel)
}

// failedSteps collects which steps (and which fans of them) of a finished workflow
// failed: the LAST attempt of each (step, fan) decides, so a step that failed once and
// succeeded on a retry counts as done. A workflow-type step counts as failed when its
// child sub-workflow ended in anything but done.
func (e *Engine) failedSteps(wfID string, spec Spec, jobs []jobstore.JobRecord) map[int]map[int]bool {
	type key struct{ step, fan int }
	latest := make(map[key]jobstore.JobRecord)
	for _, j := range jobs {
		k := key{j.StepIndex, j.FanIndex}
		if cur, ok := latest[k]; !ok || j.Attempt >= cur.Attempt {
			latest[k] = j
		}
	}
	failed := make(map[int]map[int]bool)
	for k, j := range latest {
		if j.Status == job.StatusDone || !job.IsTerminal(j.Status) {
			continue
		}
		if failed[k.step] == nil {
			failed[k.step] = make(map[int]bool)
		}
		if k.fan > 0 {
			failed[k.step][k.fan] = true
		}
	}
	for i := range spec.Steps {
		if spec.Steps[i].Type != stepTypeWorkflow {
			continue
		}
		child, found, err := e.meta.FindChildWorkflow(wfID, i+1)
		if err == nil && found && child.Status != jobstore.WorkflowDone && child.Status != jobstore.WorkflowRunning {
			if failed[i+1] == nil {
				failed[i+1] = make(map[int]bool)
			}
		}
	}
	return failed
}

// trimFailedSteps builds the `--failed` spec: the failed steps only, in their original
// order, with a failed fan-out step reduced to its failed fans (an all-failed step keeps
// every fan, and a step that failed without per-fan detail is kept whole).
func trimFailedSteps(spec Spec, failed map[int]map[int]bool) Spec {
	out := Spec{Title: spec.Title, Project: spec.Project, Vars: spec.Vars}
	for i := range spec.Steps {
		fans := failed[i+1]
		if fans == nil {
			continue
		}
		st := spec.Steps[i]
		if len(fans) > 0 {
			st = trimFailedFans(st, fans)
		}
		out.Steps = append(out.Steps, st)
	}
	return out
}

// trimFailedFans keeps only the failed fans of a fan-out step (Fan, or the positional
// Agents list). fanIdx is 1-based, matching the fan_jobs' fan_index. When the filter
// would leave nothing the step is kept whole: an empty fan list would silently turn the
// step into a single job on the step's own agent.
func trimFailedFans(st StepSpec, failed map[int]bool) StepSpec {
	if len(st.Fan) > 0 {
		kept := make([]FanSpec, 0, len(failed))
		for f := range st.Fan {
			if failed[f+1] {
				kept = append(kept, st.Fan[f])
			}
		}
		if len(kept) > 0 {
			st.Fan = kept
		}
		return st
	}
	if len(st.Agents) > 0 {
		kept := make([]string, 0, len(failed))
		for f, agent := range st.Agents {
			if failed[f+1] {
				kept = append(kept, agent)
			}
		}
		if len(kept) > 0 {
			st.Agents = kept
		}
	}
	return st
}

// trimFromStep builds the `--from` spec: step idx (1-based) and every later step, whole
// (a from-rerun re-runs all fans of the selected step). The human pick of a join=pick
// step belongs to the original step numbering, so it is dropped.
func trimFromStep(spec Spec, idx int) Spec {
	out := Spec{Title: spec.Title, Project: spec.Project, Vars: spec.Vars}
	out.Steps = append(out.Steps, spec.Steps[idx-1:]...)
	return out
}
