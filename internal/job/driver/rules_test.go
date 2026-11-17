package driver

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestDecideJobRules: one row per rule of the rule layer (plan R6), plus the ordering
// between them. The facts are what the host stored about the finished job.
func TestDecideJobRules(t *testing.T) {
	done := func(f jobFacts) jobFacts { f.Status = job.StatusDone; f.Termination = job.TerminationOK; return f }
	failed := func(termination string, f jobFacts) jobFacts {
		f.Status = job.StatusFailed
		f.Termination = termination
		return f
	}
	cases := []struct {
		name   string
		kind   string
		facts  jobFacts
		rw     reworkBudget
		action string
		rule   string
	}{
		{"done without report or check completes", job.PlanEventAdvance, done(jobFacts{}), reworkBudget{}, ActionCompleteTodo, RuleJobDoneComplete},
		{"check pass completes", job.PlanEventAdvance, done(jobFacts{CheckVerdict: "pass"}), reworkBudget{}, ActionCompleteTodo, RuleCheckPassedComplete},
		{"self pass completes", job.PlanEventAdvance, done(jobFacts{SelfDecision: job.ResultPass}), reworkBudget{}, ActionCompleteTodo, RuleSelfPassComplete},
		{"self human escalates even on done", job.PlanEventAdvance, done(jobFacts{SelfDecision: job.ResultHuman}), reworkBudget{}, ActionEscalate, RuleSelfHumanEscalate},
		{"self fail escalates even on exit 0", job.PlanEventAdvance, done(jobFacts{SelfDecision: job.ResultFail}), reworkBudget{}, ActionEscalate, RuleSelfFailEscalate},
		{"self rework resumes with notes", job.PlanEventAdvance, done(jobFacts{SelfDecision: job.ResultRework}), reworkBudget{}, ActionResumeWithNotes, RuleSelfReworkResume},
		{"check fail resumes with notes", job.PlanEventAdvance, done(jobFacts{CheckVerdict: "fail"}), reworkBudget{}, ActionResumeWithNotes, RuleCheckFailedResume},
		{"check fail beats self pass", job.PlanEventAdvance, done(jobFacts{CheckVerdict: "fail", SelfDecision: job.ResultPass}), reworkBudget{}, ActionResumeWithNotes, RuleCheckFailedResume},
		{"rework with the budget used up escalates", job.PlanEventAdvance, done(jobFacts{SelfDecision: job.ResultRework}), reworkBudget{Limit: 2, Spent: 2}, ActionEscalate, RuleReworkBudgetEscalate},
		{"rework below the ceiling still resumes", job.PlanEventAdvance, done(jobFacts{SelfDecision: job.ResultRework}), reworkBudget{Limit: 2, Spent: 1}, ActionResumeWithNotes, RuleSelfReworkResume},
		{"transient failure retries", job.PlanEventBlock, failed(job.TerminationTransient, jobFacts{}), reworkBudget{}, ActionRetry, RuleFailureTransientRetry},
		{"stalled failure retries like a transient one", job.PlanEventBlock, failed(job.TerminationStalled, jobFacts{}), reworkBudget{}, ActionRetry, RuleFailureTransientRetry},
		{"other failure escalates", job.PlanEventBlock, failed(job.TerminationAgentError, jobFacts{}), reworkBudget{}, ActionEscalate, RuleFailureOtherEscalate},
		{"self human on a failed job escalates as reported", job.PlanEventBlock, failed(job.TerminationTransient, jobFacts{SelfDecision: job.ResultHuman}), reworkBudget{}, ActionEscalate, RuleSelfHumanEscalate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := decideJob(tc.kind, tc.facts, tc.rw)
			assert.True(t, ok)
			assert.Eq(t, tc.action, d.Action)
			assert.Eq(t, tc.rule, d.Rule)
		})
	}

	// A job that is not what the event kind is about decides nothing.
	_, ok := decideJob(job.PlanEventAdvance, jobFacts{Status: job.StatusFailed}, reworkBudget{})
	assert.False(t, ok, "an advance event is about a finished job")
	_, ok = decideJob(job.PlanEventBlock, jobFacts{Status: job.StatusDone}, reworkBudget{})
	assert.False(t, ok, "a block event is about a failed job")
	_, ok = decideJob(job.PlanEventSweep, jobFacts{Status: job.StatusDone}, reworkBudget{})
	assert.False(t, ok)
}

// TestRiskTableFailsClosed: every action the rules produce has a deliberate risk, and an
// action nobody classified is a red line.
func TestRiskTableFailsClosed(t *testing.T) {
	for _, a := range []string{ActionDispatchTodo, ActionCompleteTodo, ActionResumeWithNotes, ActionRetry, ActionBlockPlan, ActionEscalate, ActionPauseBudget} {
		assert.NotEq(t, RiskRedline, RiskOf(a), a)
	}
	assert.Eq(t, RiskRedline, RiskOf("merge_to_main"))
}

// TestDispatchCandidates: the chain starts only items that are due, assigned and automatic;
// a paused plan starts nothing.
func TestDispatchCandidates(t *testing.T) {
	todo := func(id, status string, after ...string) jobstore.PlanTodo {
		return jobstore.PlanTodo{TodoID: id, Status: status, Assignee: "ok", Auto: true, After: after}
	}
	noJob := func(string) bool { return false }
	todos := []jobstore.PlanTodo{
		todo("a", jobstore.TodoDone),
		todo("b", jobstore.TodoPending, "a"), // due
		todo("c", jobstore.TodoPending, "b"), // waits
		todo("root", jobstore.TodoPending),   // a root: `plan run` starts it, not the chain
		todo("q", jobstore.TodoReady),        // queued, job never came
		{TodoID: "manual", Status: jobstore.TodoPending, Assignee: "ok", After: []string{"a"}}, // not auto
		{TodoID: "nobody", Status: jobstore.TodoPending, Auto: true, After: []string{"a"}},     // unassigned
	}
	assert.Eq(t, []string{"b", "q"}, dispatchCandidates(jobstore.Plan{}, todos, noJob))
	assert.Eq(t, []string{"b"}, dispatchCandidates(jobstore.Plan{}, todos, func(id string) bool { return id == "q" }))
	assert.Eq(t, 0, len(dispatchCandidates(jobstore.Plan{Paused: true}, todos, noJob)))
}
