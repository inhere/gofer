package driver

import (
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// observe.go compares each recorded action with what the host actually did (plan R7).
//
// What happened is read from the CURRENT state of the plan, the todo and its jobs, not
// from a diff against an earlier snapshot, which is what makes the observation repeatable
// from any later event (or from the report) and independent of when the first look was.
// The first state that reads as one of the categories below settles the action; one that
// reads as none keeps it open until ObserveWindow has passed, then it is no_followup.

// observed categories of the host's handling.
const (
	catNone     = ""
	catDispatch = "dispatch" // a job was started for a todo that had none
	catRerun    = "rerun"    // a later job on a todo whose job just ended (resume / retry / fallback)
	catComplete = "complete" // the todo was completed by its job
	catManual   = "manual"   // the todo was closed by a person after its job failed
	catSkip     = "skip"
	catBlock    = "block" // the plan parked on the todo
	catPause    = "pause" // the plan is paused or parked
)

// agrees maps an action to the categories that count as the host doing the same.
var agrees = map[string][]string{
	ActionDispatchTodo:    {catDispatch},
	ActionCompleteTodo:    {catComplete},
	ActionResumeWithNotes: {catRerun},
	ActionRetry:           {catRerun},
	ActionBlockPlan:       {catBlock},
	ActionEscalate:        {catBlock, catManual, catSkip},
	ActionPauseBudget:     {catPause},
}

func agree(action, cat string) bool {
	for _, c := range agrees[action] {
		if c == cat {
			return true
		}
	}
	return false
}

// observe settles every still-open action of the plan it can. Failures only log: an
// action that cannot be read this time is looked at again next time.
func (e *Engine) observe(planID string, now time.Time) {
	meta := e.ops.Meta()
	open, err := meta.ListPlanDriverActions(planID, true)
	if err != nil {
		slog.Warn("plan driver: list open actions", "plan_id", planID, "err", err)
		return
	}
	if len(open) == 0 {
		return
	}
	plan, ok, err := meta.GetPlan(planID)
	if err != nil || !ok {
		return
	}
	for _, a := range open {
		cat, err := e.observedCategory(plan, a)
		if err != nil {
			slog.Warn("plan driver: observe action", "plan_id", planID, "action_id", a.ID, "err", err)
			continue
		}
		outcome := ""
		switch {
		case cat != catNone && agree(a.Action, cat):
			outcome = OutcomeMatch
		case cat != catNone:
			outcome = OutcomeMismatch
		case now.Unix()-a.CreatedAt >= int64(ObserveWindow/time.Second):
			outcome = OutcomeNoFollowup
		}
		if outcome == "" {
			continue
		}
		if _, err := meta.ResolvePlanDriverAction(a.ID, outcome, now.Unix()); err != nil {
			slog.Warn("plan driver: stamp outcome", "plan_id", planID, "action_id", a.ID, "err", err)
		}
	}
}

// observedCategory reads how the host handled the situation an action was recorded for.
func (e *Engine) observedCategory(plan jobstore.Plan, a jobstore.PlanDriverAction) (string, error) {
	meta := e.ops.Meta()
	if a.TodoID == "" { // a plan-level action (the budget one)
		if plan.Paused || plan.Status == jobstore.PlanBlocked {
			return catPause, nil
		}
		return catNone, nil
	}
	blocked := plan.Status == jobstore.PlanBlocked && plan.BlockedTodo == a.TodoID
	todo, ok, err := meta.GetTodo(a.TodoID)
	if err != nil || !ok {
		return catNone, err
	}
	jobs, err := meta.ListJobsByTodo(a.TodoID, 100) // newest first
	if err != nil {
		return catNone, err
	}
	if a.JobID == "" { // dispatch_todo: did a job start?
		switch {
		case len(jobs) > 0:
			return catDispatch, nil
		case blocked:
			return catBlock, nil
		case todo.Status == jobstore.TodoSkipped:
			return catSkip, nil
		case todo.Status == jobstore.TodoDone:
			return catManual, nil
		}
		return catNone, nil
	}
	// A job-bound action: what became of that job's outcome.
	rec, ok, err := meta.GetJob(a.JobID)
	if err != nil || !ok {
		return catNone, err
	}
	later := rec.AutoResumedBy != "" || rec.FellBackTo != ""
	for _, j := range jobs {
		if j.ID == a.JobID {
			break
		}
		later = true
	}
	switch {
	case later:
		return catRerun, nil
	case blocked:
		return catBlock, nil
	case todo.Status == jobstore.TodoSkipped:
		return catSkip, nil
	case todo.Status == jobstore.TodoDone:
		if rec.Status == job.StatusDone {
			return catComplete, nil
		}
		return catManual, nil
	}
	return catNone, nil
}
