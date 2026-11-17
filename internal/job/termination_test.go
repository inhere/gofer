package job

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestTerminationReason covers every enum value: the derivation is the one place that
// answers "why did it end", so each branch has a case built from the field(s) that
// actually decide it (status, class, wrap_up, stop_reason, session_end_reason, the
// `stalled:` prefix).
func TestTerminationReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   JobResult
		want string
	}{
		{"done plain", JobResult{Status: StatusDone}, TerminationOK},
		{"done with acp end_turn", JobResult{Status: StatusDone, StopReason: "end_turn"}, TerminationOK},
		{"failed other", JobResult{Status: StatusFailed, FailureClass: FailureClassOther}, TerminationAgentError},
		{"failed unclassified", JobResult{Status: StatusFailed}, TerminationAgentError},
		{"failed transient", JobResult{Status: StatusFailed, FailureClass: FailureClassTransient}, TerminationTransient},
		{"stalled beats transient class", JobResult{Status: StatusFailed, FailureClass: FailureClassTransient, Error: "stalled: no output for 120s"}, TerminationStalled},
		{"stalled on cancel", JobResult{Status: StatusCancelled, Error: "stalled: no output for 5s"}, TerminationStalled},
		{"status timeout", JobResult{Status: StatusTimeout}, TerminationTimeout},
		{"failed wrap_up timeout", JobResult{Status: StatusFailed, WrapUp: wrapUpTimeout}, TerminationTimeout},
		{"done wrap_up timeout", JobResult{Status: StatusDone, WrapUp: wrapUpTimeout}, TerminationTimeout},
		{"status failed budget", JobResult{Status: StatusFailed, FailureClass: FailureClassBudget}, TerminationBudget},
		{"done agent wrap_up budget", JobResult{Status: StatusDone, WrapUp: wrapUpBudget}, TerminationBudget},
		{"done acp max_tokens", JobResult{Status: StatusDone, StopReason: stopReasonMaxTokens}, TerminationBudget},
		{"done acp max_turn_requests", JobResult{Status: StatusDone, StopReason: stopReasonMaxTurnRequests}, TerminationBudget},
		{"done wrap_up max_steps", JobResult{Status: StatusDone, WrapUp: "max_steps"}, TerminationAgentError},
		{"done acp refusal", JobResult{Status: StatusDone, StopReason: stopReasonRefusal}, TerminationAgentError},
		{"done acp cancelled", JobResult{Status: StatusDone, StopReason: stopReasonCancelled}, TerminationCancelled},
		{"cancelled", JobResult{Status: StatusCancelled}, TerminationCancelled},
		{"cancelled manual end", JobResult{Status: StatusCancelled, SessionEndReason: "manual_end"}, TerminationCancelled},
		{"session idle timeout", JobResult{Status: StatusCancelled, SessionEndReason: "idle_timeout"}, TerminationSessionEnded},
		{"session max session", JobResult{Status: StatusCancelled, SessionEndReason: "max_session"}, TerminationSessionEnded},
		{"rejected", JobResult{Status: StatusRejected}, TerminationAgentError},
		{"worktree gone", JobResult{Status: StatusFailed, FailureClass: FailureClassWorktreeGone}, TerminationWorktreeGone},
		{"caller disabled", JobResult{Status: StatusFailed, FailureClass: FailureClassCallerDisabled}, TerminationCallerDisabled},
		{"still running", JobResult{Status: StatusRunning}, TerminationUnknown},
		{"awaiting review", JobResult{Status: StatusNeedsReview}, TerminationUnknown},
		{"empty", JobResult{}, TerminationUnknown},
	}
	for _, tc := range cases {
		if got := TerminationReason(tc.in); got != tc.want {
			t.Errorf("%s: TerminationReason = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDiagnoseChainEarliestFailure asserts the chain walk finds every link (resume,
// failover, durable retry, source) and that the reported first failure is the EARLIEST
// one — the origin, not the last attempt.
func TestDiagnoseChainEarliestFailure(t *testing.T) {
	t.Parallel()
	jobs := map[string]JobResult{
		"job-a": {ID: "job-a", Status: StatusFailed, FailureClass: FailureClassOther,
			Error: "boom: first thing broke\nsecond line", StartedAt: 100, EndedAt: 110, AutoResumedBy: "job-b"},
		"job-b": {ID: "job-b", Status: StatusFailed, FailureClass: FailureClassTransient,
			ResumedFrom: "job-a", StartedAt: 200, EndedAt: 210, FellBackTo: "job-c"},
		"job-c": {ID: "job-c", Status: StatusFailed, FailureClass: FailureClassOther,
			FellBackFrom: "job-b", StartedAt: 300, EndedAt: 310},
	}
	retries := map[string][]jobstore.RetryRecord{
		"job-c": {{ID: "r1", SourceJobID: "job-c", Attempt: 2, NewJobID: "job-d", State: "done"}},
	}
	jobs["job-d"] = JobResult{ID: "job-d", Status: StatusDone, StartedAt: 400, EndedAt: 410}

	get := func(id string) (JobResult, bool) { r, ok := jobs[id]; return r, ok }
	rows := func(id string) []jobstore.RetryRecord { return retries[id] }

	chain := diagnoseChainWith("job-c", get, rows)
	if len(chain) != 4 {
		t.Fatalf("chain has %d links, want 4: %+v", len(chain), chain)
	}
	for i, want := range []string{"job-a", "job-b", "job-c", "job-d"} {
		if chain[i].JobID != want {
			t.Fatalf("chain[%d] = %s, want %s (oldest first)", i, chain[i].JobID, want)
		}
	}
	if chain[0].Relation != "resume" || chain[0].Error != "boom: first thing broke" {
		t.Fatalf("link a: relation=%q error=%q", chain[0].Relation, chain[0].Error)
	}
	first := firstFailingLink(chain, "job-c")
	if first == nil || first.JobID != "job-a" {
		t.Fatalf("first failure = %+v, want job-a", first)
	}
}

// TestDiagnoseChainSkipsMissingLink asserts a deleted link is named instead of silently
// dropped, and that a cycle cannot loop forever.
func TestDiagnoseChainSkipsMissingLink(t *testing.T) {
	t.Parallel()
	jobs := map[string]JobResult{
		"job-x": {ID: "job-x", Status: StatusFailed, ResumedFrom: "job-gone", AutoResumedBy: "job-x"},
	}
	get := func(id string) (JobResult, bool) { r, ok := jobs[id]; return r, ok }
	chain := diagnoseChainWith("job-x", get, func(string) []jobstore.RetryRecord { return nil })
	if len(chain) != 2 {
		t.Fatalf("chain has %d links, want 2: %+v", len(chain), chain)
	}
	byID := map[string]DiagnosisLink{}
	for _, l := range chain {
		byID[l.JobID] = l
	}
	if got := byID["job-gone"]; got.Status != "missing" {
		t.Fatalf("missing link: %+v", got)
	}
}
