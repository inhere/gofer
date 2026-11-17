package job

import (
	"strings"

	"github.com/inhere/gofer/internal/i18n"
)

// TerminationReason is the ONE derived answer to "why did this job end?", written as a
// stable enum so a caller (the diagnosis endpoint, `job diagnose`, the web detail) can
// branch on it and a person can read the localized label (TerminationLabelIn).
//
// It is DERIVED from the fields a job already carries — status, failure_class,
// stop_reason, wrap_up, session_end_reason, the `stalled:` error prefix and cancels —
// and never stored: a new column would be a second source of truth for the same fact,
// and the fields it is computed from are the ones the executor itself decided with.
// Because it is derived, this stays a pure function: no service, no clock, no config.
const (
	// TerminationOK: the job finished normally and has nothing to explain.
	TerminationOK = "ok"
	// TerminationAgentError: the work itself failed (a bug, a bad command, a verify step
	// that did not pass, a refusal). Another agent would meet the same problem.
	TerminationAgentError = "agent_error"
	// TerminationTransient: a provider-side error (failure_class=transient); a fresh
	// process may well succeed.
	TerminationTransient = "transient"
	// TerminationStalled: the AUTO-05 watchdog killed it for producing no output inside
	// its stall window. It reads as TRANSIENT for the continuation chain (the error text
	// starts with "stalled:"), so it is matched BEFORE the class.
	TerminationStalled = "stalled"
	// TerminationTimeout: the job's deadline (or the agent's own wall-clock wrap-up) ran
	// out.
	TerminationTimeout = "timeout"
	// TerminationBudget: a token / cost / turn ceiling stopped the run — either the
	// gofer budget meter (failure_class=budget) or the agent's own soft budget
	// (wrap_up / stop_reason reporting a token or turn ceiling).
	TerminationBudget = "budget"
	// TerminationCancelled: somebody cancelled it (a human, `job cancel`, an end of
	// session), and nothing else explains the end better.
	TerminationCancelled = "cancelled"
	// TerminationWorktreeGone: it was refused because the worktree it had to continue in
	// no longer exists (failure_class=worktree-gone).
	TerminationWorktreeGone = "worktree_gone"
	// TerminationCallerDisabled: it was refused because the caller owning it is disabled;
	// the work never ran (failure_class=caller-disabled).
	TerminationCallerDisabled = "caller_disabled"
	// TerminationSessionEnded: a persistent session was ended by its own lifecycle
	// (idle timeout, max session time), not by a decision about this one job.
	TerminationSessionEnded = "session_ended"
	// TerminationUnknown: nothing recorded explains the end — the job has not finished, or
	// it ended without any of the fields above. Never guessed further.
	TerminationUnknown = "unknown"
)

// Wire values this file compares against. They are persistence-format strings
// (jobs.stop_reason / jobs.wrap_up / session_state_json), so they are spelled here
// instead of importing the runner / protocol packages into the job model.
const (
	stopReasonCancelled       = "cancelled"
	stopReasonRefusal         = "refusal"
	stopReasonMaxTokens       = "max_tokens"
	stopReasonMaxTurnRequests = "max_turn_requests"
	wrapUpBudget              = "budget"
	wrapUpTimeout             = "timeout"
)

// TerminationReason derives a job's termination reason (see the enum above). It is a
// pure function of the persisted/queryable job state, so the same row always answers
// the same reason — after a restart, in the CLI and in the web console alike.
func TerminationReason(r JobResult) string {
	// A refusal never ran or never ran to completion: the class says WHY, and it beats
	// every status-based reading (e.g. a refused submit is status=failed with no error).
	switch r.FailureClass {
	case FailureClassWorktreeGone:
		return TerminationWorktreeGone
	case FailureClassCallerDisabled:
		return TerminationCallerDisabled
	}
	if strings.HasPrefix(strings.TrimSpace(r.Error), "stalled:") {
		return TerminationStalled
	}
	switch r.Status {
	case StatusDone:
		return doneTerminationReason(r)
	case StatusFailed:
		switch r.FailureClass {
		case FailureClassBudget:
			return TerminationBudget
		case FailureClassTransient:
			return TerminationTransient
		}
		switch r.WrapUp {
		case wrapUpBudget:
			return TerminationBudget
		case wrapUpTimeout:
			return TerminationTimeout
		}
		return TerminationAgentError
	case StatusTimeout:
		return TerminationTimeout
	case StatusCancelled:
		// A session's own lifecycle (idle timeout / max session time) ends the running
		// turn, which lands here as a cancel. It is not a decision about the work, so it
		// keeps its own reason; a human's `manual_end` stays a plain cancel.
		switch r.SessionEndReason {
		case "idle_timeout", "max_session", "max_session_timeout":
			return TerminationSessionEnded
		}
		return TerminationCancelled
	case StatusRejected:
		// A human refused the delivery: the work is what did not hold up.
		return TerminationAgentError
	}
	// queued / running / waiting_* / needs_review / awaiting_* — nothing has ended yet.
	return TerminationUnknown
}

// doneTerminationReason explains a job that ENDED NORMALLY but may still have stopped
// short: the agent's own wrap-up (it wound the run up instead of answering) and the ACP
// prompt's stopReason (it refused, or hit a protocol ceiling).
func doneTerminationReason(r JobResult) string {
	switch r.WrapUp {
	case wrapUpBudget:
		return TerminationBudget
	case wrapUpTimeout:
		return TerminationTimeout
	case "":
		// fall through to the ACP stopReason below
	default:
		// max_steps / max_tokens / anything else the agent reported: it stopped itself
		// before answering, so the report is worth reading but the work is unfinished.
		return TerminationAgentError
	}
	switch r.StopReason {
	case stopReasonCancelled:
		return TerminationCancelled
	case stopReasonRefusal:
		return TerminationAgentError
	case stopReasonMaxTokens, stopReasonMaxTurnRequests:
		return TerminationBudget
	}
	return TerminationOK
}

// TerminationLabelIn is a short human label of reason in lang (job.termination.<reason>).
// An unknown reason is returned unchanged, exactly like StatusLabelIn.
func TerminationLabelIn(lang i18n.Lang, reason string) string {
	if key := "job.termination." + reason; reason != "" && i18n.HasKey(lang, key) {
		return i18n.T(lang, key)
	}
	return reason
}
