package job

import (
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Automatic child registration (design 2026-11-03-resident-job-child-wakeup 范围 2).
//
// A resident ACP job that submits another job with its own job credential gets a wakeup
// registered for it: when the child reaches a state the parent has to know about, the
// parent is woken with a receipt (see wakeup_session.go). Nothing is registered for a
// caller that is not a resident job's own credential — that is why the parent id is an
// internal request field the HTTP entry fills from the credential and never a wire
// field: a request body must not be able to nominate somebody else's job as the parent.

// registerChildWakeup stores the receipt row for a child job a resident parent submitted.
// It is called AFTER the child's row is persisted (so the receipt can reference a job
// that exists) and BEFORE the child's own events are recorded (so a job.awaiting_approval
// that parks the child already has its recipient).
//
// Nothing is registered when:
//   - the submission is not a job credential's (parentJobID is empty);
//   - the job is the re-entry of a hold or an update park (heldJobID set): it was
//     registered when it was parked, and its parent id does not survive the round trip
//     through request_json (the field is internal);
//   - the parent asked not to be woken (`--no-parent-wake`) or waited for the result
//     itself (`?wait=1`: the parent already has it);
//   - the parent is not a resident session, or is already finished;
//   - the parent's agent/role has no can_submit grant (it could not have submitted).
//
// mode is continuous: the receipt is retired by the child's terminal outcome (see
// confirmSayBatchLocked), and a needs-review child keeps it armed for the later verdict.
func (s *Service) registerChildWakeup(parentJobID string, child JobResult, req JobRequest) error {
	if parentJobID == "" || req.heldJobID != "" || req.NoParentWake || req.ParentWakeSync {
		return nil
	}
	parent, ok := s.Get(parentJobID)
	if !ok || !parent.Session || IsFinished(parent.Status) {
		return nil
	}
	if canSubmit, _ := s.SubmitGrant(parent); !canSubmit {
		return nil
	}
	cfg := s.config()
	events, err := cfg.ResolveChildEvents(req.ParentWakeEvents)
	if err != nil {
		// The entry layer validates the same list, so this is a config that changed
		// between the submit and here: refuse the receipt, never the job that is running.
		return err
	}
	now := s.nowFn()
	// TTL: long enough for the child to queue (a directory lock can wait an hour) and to
	// run its own limit, whichever is longer than the configured wakeup lifetime.
	ttl := cfg.WakeupTTL()
	if need := time.Duration(childWakeLimitSec(child, req)+childWakeQueueMarginSec) * time.Second; need > ttl {
		ttl = need
	}
	w := jobstore.WakeupRecord{
		ID:             "wk-" + RandomSuffix(),
		JobID:          parent.ID,
		Kind:           jobstore.WakeupKindEvent,
		EventTypesJSON: jobstore.EncodeStringList(events),
		FilterJobID:    child.ID,
		Mode:           jobstore.WakeupModeContinuous,
		Enabled:        1,
		Revision:       1,
		AutoChild:      1,
		// The parent's caller owns the receipt: the job credential's own identity is not a
		// caller anybody could act as later (design 架构: CreatedBy 用父 job 的 CallerID).
		CreatedBy: parent.CallerID,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
	}
	if err := s.meta.InsertWakeup(w); err != nil {
		return err
	}
	slog.Info("wakeup.created", "event", "wakeup.created", "wakeup_id", w.ID,
		"job_id", w.JobID, "kind", w.Kind, "filter_job_id", w.FilterJobID, "auto_child", 1)
	return nil
}

// childWakeLimitSec is how long the child itself may run: the resolved limit when the row
// already carries one, else the limit the caller asked (and the admission clamped) for.
func childWakeLimitSec(child JobResult, req JobRequest) int {
	sec := child.TimeoutSec
	if sec <= 0 {
		sec = req.TimeoutSec
	}
	if req.IdleTimeoutSec > 0 {
		sec += req.IdleTimeoutSec
	}
	if sec <= 0 {
		sec = childWakeQueueMarginSec // no deadline at all: rely on the modest default above
	}
	return sec
}
