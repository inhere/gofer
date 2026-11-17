// Package driver is the plan driver (design
// docs/design/2026-11-16-plan-driver-autonomy-design.md §1, plan R3 / R5-R8): the part of
// gofer that watches a plan's progression events and works out what it WOULD do about
// each, in the `record` level only observing — it writes its own tables (the action log
// and the plan driver's accounting) and nothing else. No plan, todo or job is ever
// changed from here: that is the invariant this package exists to keep, and why HostOps
// hands it a store and a clock but no dispatch, resume or block entry.
//
// Layering (G024, the job/workflow shape): internal/job defines job.PlanDriver and calls
// it from its plan event handler without importing this package; Engine implements the
// interface and reaches the host through HostOps. core wires the two.
package driver

import (
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Actions the driver can record (plan R5).
const (
	ActionDispatchTodo    = "dispatch_todo"
	ActionCompleteTodo    = "complete_todo"
	ActionResumeWithNotes = "resume_with_notes"
	ActionRetry           = "retry"
	// ActionBlockPlan is part of the vocabulary; no rule of this version produces it (a
	// failed chain job is an escalate: the plan parking itself is how the host reacts).
	ActionBlockPlan = "block_plan"
	// ActionEscalate is "the rules cannot decide, a human should". It is only recorded in
	// this version; it becomes a decision point later.
	ActionEscalate    = "escalate"
	ActionPauseBudget = "pause_budget"
)

// Risk levels of an action (design §1 "动作白名单与风险").
const (
	RiskLow     = "low"
	RiskMedium  = "medium"
	RiskRedline = "redline"
)

// riskOf is the fixed risk table. It lives in code on purpose: configuration must not be
// able to widen it. An action missing from the table is a red line, so a new action added
// without a deliberate risk entry fails closed.
var riskOf = map[string]string{
	ActionDispatchTodo:    RiskLow,
	ActionCompleteTodo:    RiskLow,
	ActionResumeWithNotes: RiskLow,
	ActionRetry:           RiskLow,
	ActionEscalate:        RiskLow,
	ActionBlockPlan:       RiskMedium,
	ActionPauseBudget:     RiskMedium,
}

// RiskOf returns the risk of an action; an unknown action is a red line.
func RiskOf(action string) string {
	if r, ok := riskOf[action]; ok {
		return r
	}
	return RiskRedline
}

// Rule names (plan R6): every recorded action says which rule produced it.
const (
	RuleDispatchReady         = "dispatch_ready"
	RuleJobDoneComplete       = "job_done_complete"
	RuleCheckPassedComplete   = "check_passed_complete"
	RuleSelfPassComplete      = "self_report_pass_complete"
	RuleSelfHumanEscalate     = "self_report_human"
	RuleSelfFailEscalate      = "self_report_fail"
	RuleSelfReworkResume      = "self_report_rework"
	RuleCheckFailedResume     = "check_failed_resume"
	RuleReworkBudgetEscalate  = "rework_budget_exhausted"
	RuleFailureTransientRetry = "failure_transient_retry"
	// RuleFailureInterruptedRetry: gofer itself went away under the job (a serve restart
	// failed it as an orphan). Nothing is known about the work, so it is not the agent's
	// failure: rerun it, like a transient one.
	RuleFailureInterruptedRetry = "failure_interrupted_retry"
	RuleFailureOtherEscalate    = "failure_other_escalate"
	RuleBudgetExhausted         = "budget_exhausted"
	RuleNeedsReviewEscalate     = "needs_review_escalate"
)

// Outcomes stamped on an action once what actually happened is known (plan R7).
const (
	OutcomeMatch      = "match"
	OutcomeMismatch   = "mismatch"
	OutcomeNoFollowup = "no_followup"
)

// ObserveWindow is how long an action is watched for the host's own handling before it
// reads no_followup.
const ObserveWindow = 24 * time.Hour

// HostOps is what the driver needs from the host, structurally satisfied by *job.Service
// (Meta / Now are its read accessors, the same ones the workflow engine uses). It is
// deliberately a store and a clock: the driver reads plans, todos and jobs and writes its
// own tables, and has no handle to change any of them. Tests pass their own Now to move
// the observation window without sleeping (G052).
type HostOps interface {
	Meta() *jobstore.Store
	Now() time.Time
}
