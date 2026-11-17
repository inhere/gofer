package driver

import (
	"strings"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// rules.go is the deterministic rule layer (plan R6). It is pure: facts in, at most one
// decision out, no store and no clock, so every rule is a plain table-driven test.

// Decision is one action a rule picked, before it is stored.
type Decision struct {
	Action string
	Rule   string
	TodoID string
	JobID  string
	Params map[string]any
}

// jobFacts are the facts about one finished job the rules read.
type jobFacts struct {
	// Status is the job's terminal status.
	Status string
	// Termination is job.TerminationReason of the job.
	Termination string
	// SelfDecision / SelfReason are the job's self-reported outcome ("" = it reported none).
	SelfDecision string
	SelfReason   string
	// CheckVerdict is the delivery check's verdict: "pass", "fail" or "" (no check ran).
	CheckVerdict string
}

// reworkBudget is the rework ceiling and what is spent against it; Limit 0 is unlimited.
type reworkBudget struct {
	Limit int
	Spent int
}

func (r reworkBudget) exhausted() bool { return r.Limit > 0 && r.Spent >= r.Limit }

func isFailureStatus(st string) bool {
	switch st {
	case job.StatusFailed, job.StatusTimeout, job.StatusCancelled, job.StatusRejected:
		return true
	}
	return false
}

// decideJob applies the rules to a job's event. kind is the plan event kind: a `block`
// event is about a FAILED chain job, an `advance` event carrying a job is about one that
// finished and completed its todo. Anything else decides nothing.
//
// Order: the job's own words come first (a `human` or `fail` report is a stop even on
// exit 0), then the delivery check's verdict, then the exit code.
func decideJob(kind string, f jobFacts, rw reworkBudget) (Decision, bool) {
	failed := kind == job.PlanEventBlock && isFailureStatus(f.Status)
	done := kind == job.PlanEventAdvance && f.Status == job.StatusDone
	// An observe event is a job that ended without moving its item: waiting for a human
	// verdict, or done with the item still open. Only the strong signals decide for the
	// latter; the former escalates when nothing stronger speaks.
	review := kind == job.PlanEventObserve && f.Status == job.StatusNeedsReview
	open := kind == job.PlanEventObserve && f.Status == job.StatusDone
	if !failed && !done && !review && !open {
		return Decision{}, false
	}
	params := map[string]any{"status": f.Status, "termination": f.Termination}
	if f.SelfDecision != "" {
		params["self_decision"] = f.SelfDecision
		if f.SelfReason != "" {
			params["self_reason"] = clip(f.SelfReason, 200)
		}
	} else {
		params["self_decision"] = "missing"
	}
	if f.CheckVerdict != "" {
		params["check_verdict"] = f.CheckVerdict
	}
	d := func(action, rule string) (Decision, bool) {
		return Decision{Action: action, Rule: rule, Params: params}, true
	}
	resume := func(rule string) (Decision, bool) {
		if rw.exhausted() {
			params["rework_limit"] = rw.Limit
			params["rework_spent"] = rw.Spent
			return d(ActionEscalate, RuleReworkBudgetEscalate)
		}
		return d(ActionResumeWithNotes, rule)
	}
	switch f.SelfDecision {
	case job.ResultHuman:
		return d(ActionEscalate, RuleSelfHumanEscalate)
	case job.ResultFail:
		return d(ActionEscalate, RuleSelfFailEscalate)
	}
	if (done || review || open) && f.CheckVerdict == "fail" {
		return resume(RuleCheckFailedResume)
	}
	if f.SelfDecision == job.ResultRework {
		return resume(RuleSelfReworkResume)
	}
	if review {
		return d(ActionEscalate, RuleNeedsReviewEscalate)
	}
	if open {
		return Decision{}, false
	}
	if failed {
		switch f.Termination {
		case job.TerminationTransient, job.TerminationStalled:
			return d(ActionRetry, RuleFailureTransientRetry)
		}
		return d(ActionEscalate, RuleFailureOtherEscalate)
	}
	switch {
	case f.CheckVerdict == "pass":
		return d(ActionCompleteTodo, RuleCheckPassedComplete)
	case f.SelfDecision == job.ResultPass:
		return d(ActionCompleteTodo, RuleSelfPassComplete)
	}
	return d(ActionCompleteTodo, RuleJobDoneComplete)
}

// dispatchCandidates lists the todos the chain would start at this event: waiting items
// whose dependencies are done or skipped (a root is started by `plan run`, never by the
// chain), and items already queued whose job never came. Unassigned and manual (non-auto)
// items are nobody's to start. It reads the state BEFORE the host advances, which is why
// the engine calls it from BeforePlanEvent.
func dispatchCandidates(p jobstore.Plan, todos []jobstore.PlanTodo, hasJob func(todoID string) bool) []string {
	if p.Paused {
		return nil
	}
	byID := make(map[string]jobstore.PlanTodo, len(todos))
	for _, t := range todos {
		byID[t.TodoID] = t
	}
	var out []string
	for _, t := range todos {
		if t.Assignee == "" || !t.Auto {
			continue
		}
		switch t.Status {
		case jobstore.TodoPending:
			if len(t.After) > 0 && depsSatisfied(t, byID) {
				out = append(out, t.TodoID)
			}
		case jobstore.TodoReady:
			if t.DispatchError == "" && !hasJob(t.TodoID) {
				out = append(out, t.TodoID)
			}
		}
	}
	return out
}

// depsSatisfied mirrors the host's rule: every item it waits for is done or skipped, and an
// id the plan does not hold counts as not satisfied.
func depsSatisfied(t jobstore.PlanTodo, byID map[string]jobstore.PlanTodo) bool {
	for _, dep := range t.After {
		d, ok := byID[dep]
		if !ok || (d.Status != jobstore.TodoDone && d.Status != jobstore.TodoSkipped) {
			return false
		}
	}
	return true
}

// clip cuts s to at most n runes.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
