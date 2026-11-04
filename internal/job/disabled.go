// Disabled callers on the JOB side (配置入库 P1, design
// docs/design/2026-11-03-config-into-db-design.md §停用, plan T5).
//
// A caller stored in the database can be switched off from the console without a restart.
// "Switched off" has to hold on every path that can start or continue work on that caller's
// behalf, so the question is asked in three places, all of them through config.OwnerBlocked:
//
//   - admission (admitCeiling), BEFORE the ceiling is looked up: a disabled caller has no
//     ceiling, and the "no ceiling, nothing to check" answer must not admit it;
//   - the owner a derived submission inherits (inheritCeilingOwner);
//   - the re-judgment in execute, after the last queue (sem await + directory lock): a job
//     admitted before the disable must not start, and must not be retried afterwards.
//
// A resumed resident session (a server restart bringing back a live session) deliberately
// carries no re-check owner (D2): it is already running, so it keeps running until it ends or
// a human cancels it; the jobs it submits go through admission and are refused there.
package job

import (
	"errors"
	"fmt"
	"slices"
)

// errCallerDisabled is the PERMANENT refusal of work owned by a disabled caller. Its TEXT is
// the only thing that survives on a persisted row (JobResult.Error is a string), so
// failureDecision recognises the class from the same fixed text — the way a budget breach is
// recognised from its own message (see runner.ParseBudgetBreach).
var errCallerDisabled = errors.New("caller disabled")

// errCallerDisabledFor builds the refusal naming the owner. Admission wraps it in
// ErrCeilingDenied (so every re-submission path terminates on it); the execute-time re-judgment
// hands it to finish() as the job's error.
func errCallerDisabledFor(owner string) error {
	return fmt.Errorf("%w: caller %q is disabled", errCallerDisabled, owner)
}

// disabledExecReleased lists the internal exec kinds that are STILL allowed for a disabled owner
// (D7). They are the closing acts of a job that already ran — the delivery of its work, and the
// read-only check of that delivery — and refusing them would leave finished work undelivered
// (or an interactive session unable to end). Everything else an internal exec does starts NEW
// work (a relay message, a tracker sync, a summarizer run, a workflow step), so it is refused.
var disabledExecReleased = []string{InternalExecDelivery, InternalExecDeliveryCheck}

// disabledExecReleasedFor reports whether req is one of the released internal exec kinds.
func disabledExecReleasedFor(req JobRequest) bool {
	return req.InternalExec != "" && slices.Contains(disabledExecReleased, req.InternalExec)
}

// disabledRecheckOwners returns the owners execute re-judges once the job has passed its last
// queue: the same owners admission judged (ceilingOwners), and none for a system job or a
// released internal exec — a system job carries no caller at all, and the released internal exec
// is allowed to finish. An empty result means "not re-judged".
func disabledRecheckOwners(req JobRequest) []string {
	if req.SystemJob != "" || disabledExecReleasedFor(req) {
		return nil
	}
	return ceilingOwners(req)
}

// disabledOwnerAmong returns the first owner of owners that the CURRENT configuration says must
// not own a job, or "" when none of them is blocked. It reads the live config snapshot, exactly
// as admission does, so a disable is observed by the next re-judgment without a restart.
func (s *Service) disabledOwnerAmong(owners []string) string {
	if len(owners) == 0 {
		return ""
	}
	cfg := s.config()
	if cfg == nil {
		return ""
	}
	for _, owner := range owners {
		if cfg.OwnerBlocked(owner) {
			return owner
		}
	}
	return ""
}
