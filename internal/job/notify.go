package job

// notify.go is the submit-side half of gofer-3db4 ("tell the session that sent
// this job when the job ends"). The DELIVERY lives in internal/sessionrelay (the
// session messenger is its domain); this file only decides, at submit time, WHICH
// session a finished job must tell and makes that session's pending record exist.
//
// The pending record is the existing session job watch — the same row the Stop
// hook and its catch-up read. That choice is what makes the notification
// at-most-once across a server restart without a new column or a second table:
// the watch row IS the queue entry, and whoever claims it (delete succeeds) owns
// the notice.

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// notifyTargetKinds are the two accepted spellings of "enable the notification":
// the flag/field itself, or an explicit target session (which implies it).
func notifyRequested(req JobRequest) bool {
	return req.Notify || strings.TrimSpace(req.NotifySession) != ""
}

// notifyTarget is the session a finished job tells: the explicit NotifySession
// when given, else the job's authenticated source session. "" means nobody.
func notifyTarget(req JobRequest) string {
	if !notifyRequested(req) {
		return ""
	}
	if sid := strings.TrimSpace(req.NotifySession); sid != "" {
		return sid
	}
	return strings.TrimSpace(req.SourceSessionID)
}

// notifyTargetJobID reports whether target CAN be a job id of this server: a job id is
// recognizable by its prefix before anything is looked up. It only narrows the search —
// an agent session id may start with the same letters, so an id that resolves as a
// session is always treated as one (see notifySessionJobReason).
func notifyTargetJobID(target string) bool { return strings.HasPrefix(target, JobIDPrefix) }

// notifySessionJobReason answers "may a finished job tell this session job?": "" when the
// target is a live persistent session of this server, else the reason it is not (the
// same wording for an unknown target whether it was meant as a job id or a session id).
func (s *Service) notifySessionJobReason(target, callerID string) string {
	if !notifyTargetJobID(target) {
		return fmt.Sprintf("notify session %q is not a registered session of this server", target)
	}
	res, ok := s.Get(target)
	switch {
	case !ok:
		return fmt.Sprintf("notify session %q is neither a registered session nor a job of this server", target)
	case !res.Session:
		return fmt.Sprintf("job %q is not a persistent session: pass the session id of an agent session, or the job id of a --session job", target)
	case IsFinished(res.Status):
		return fmt.Sprintf("session job %q has already ended (status %s)", target, res.Status)
	case res.CallerID != "" && strings.TrimSpace(callerID) != "" && res.CallerID != strings.TrimSpace(callerID):
		// Same rule as an agent session target: a result is spoken into the session as a
		// turn, so only the session's own caller may aim one at it.
		return fmt.Sprintf("session job %q belongs to another caller", target)
	}
	return ""
}

// droppedNotify is an end-notification a submit could not honour and gave up
// (gofer-3db4): the resolved target and why it is unusable. Submit records it as a
// job.notify_dropped event, so the job's own timeline answers "why was I not told?".
type droppedNotify struct {
	Target string
	Reason string
}

// resolveNotifyRequest decides what happens to a request's end-notification:
//
//   - nothing to do: it was not requested, or its target is the job's own source
//     session (proven by validateSourceSession, which runs just before this);
//   - honoured: the target exists and may be told about this caller's job;
//   - DROPPED (gofer-3db4): an internal producer's request (NotifyDegradable — a
//     schedule tick, a plan dispatch, a workflow step, a wakeup / auto-resume /
//     rerun continuation) named a target nobody can address. The notification is
//     removed from the request and the reason is returned for the job event:
//     refusing the work because a notice could not be sent would lose a job its
//     sender never asked about. A caller who named the flag itself gets an error
//     instead — their mistake belongs to them, not to a timeline entry.
//
// It runs with — and after — validateSourceSession, so a target that IS the source
// session has already been proven to belong to the authenticated caller. An explicit
// target owned by someone else is refused (or dropped) for the same reason a source
// session is: telling another caller's terminal about your job is the same
// cross-caller write.
func (s *Service) resolveNotifyRequest(req *JobRequest) (*droppedNotify, error) {
	if !notifyRequested(*req) {
		return nil, nil
	}
	target := notifyTarget(*req)
	var reason string
	switch {
	case target == "":
		reason = "no session to tell: the request named no notify_session and has no source session"
	case target == strings.TrimSpace(req.SourceSessionID):
		return nil, nil // proven by validateSourceSession
	default:
		session, ok, err := s.meta.GetAgentSession(target)
		switch {
		case err != nil:
			// A store failure is not an unusable target: it is this server's own
			// problem, and the caller (internal producer included) needs to see it.
			return nil, fmt.Errorf("load notify session: %w", err)
		case !ok:
			// Not a registered agent session. It may be a persistent session named by its
			// JOB id (`--notify-session <job id>`), which is delivered as that session's
			// next turn instead of by the messenger (see registerNotifySessionReceipt).
			reason = s.notifySessionJobReason(target, req.CallerID)
		case session.CallerID != "" && strings.TrimSpace(req.CallerID) != "" && session.CallerID != strings.TrimSpace(req.CallerID):
			reason = fmt.Sprintf("notify session %q belongs to another caller", target)
		}
	}
	if reason == "" {
		return nil, nil // the target exists and may be told about this caller's job
	}
	if req.NotifyDegradable {
		req.Notify, req.NotifySession = false, ""
		return &droppedNotify{Target: target, Reason: reason}, nil
	}
	if target == "" {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, notifyTargetError("notify needs a session to tell: pass --notify-session <sid> (or notify_session), set the source session, or run it from the agent session itself"))
	}
	return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, notifyTargetError(reason))
}

// ErrNotifyTargetRejected identifies a submit refused only because its
// end-notification target is unusable (unknown session, or owned by another
// caller). The HTTP layer reports it as ErrCodeNotifyTargetRejected so a client can
// tell it apart from every other 400 without matching message text.
var ErrNotifyTargetRejected = errors.New("notify target rejected")

// ErrCodeNotifyTargetRejected is the stable "code" of the error body for
// ErrNotifyTargetRejected.
const ErrCodeNotifyTargetRejected = "notify_target_rejected"

// notifyTargetError carries the human reason unchanged and matches
// ErrNotifyTargetRejected under errors.Is.
type notifyTargetError string

func (e notifyTargetError) Error() string { return string(e) }

func (e notifyTargetError) Is(target error) bool { return target == ErrNotifyTargetRejected }

// watchNotifyTarget registers the notify target's pending record, so the terminal
// notify has something durable to claim. It is a no-op when the target is the
// source session (Submit has already added that watch) or when notify is off.
//
// A target that is a session JOB id gets the receipt row of a resident parent's child
// instead (see registerNotifySessionReceipt): there is no registered agent session to
// watch, and the result has to arrive as the session's next turn.
func (s *Service) watchNotifyTarget(req JobRequest, jobID string) error {
	target := notifyTarget(req)
	if target == "" || target == strings.TrimSpace(req.SourceSessionID) {
		return nil
	}
	if notifyTargetJobID(target) {
		if _, isSession, err := s.meta.GetAgentSession(target); err != nil || !isSession {
			// Not an agent session: a live persistent session JOB gets the spoken receipt.
			return s.registerNotifySessionReceipt(req, target, jobID)
		}
	}
	if _, err := s.meta.AddSessionJobWatch(target, jobID); err != nil {
		return fmt.Errorf("register notify session watch: %w", err)
	}
	return nil
}

// registerNotifySessionReceipt gives `--notify-session <job id>` the row a resident
// parent gets for a child it submitted (wakeup_child_register.go): an event wakeup on
// the session job that the say path speaks into the session as its next turn.
//
// Reusing that row is the whole point — it is what parks the result while the session
// is in a turn, delivers it when the session comes back to awaiting_input, follows a
// takeover / retry onto the job that carries the real outcome (auto_child), and reports
// it to a person when the session is gone.
func (s *Service) registerNotifySessionReceipt(req JobRequest, target, jobID string) error {
	if rows, err := s.meta.ListEventWakeupsByChild(jobID); err == nil {
		for _, w := range rows {
			if w.JobID == target {
				return nil // a retried submit must not stack a second receipt
			}
		}
	}
	child, ok := s.Get(jobID)
	if !ok {
		return fmt.Errorf("register notify session receipt: job %s is gone", jobID)
	}
	now := s.nowFn()
	// Same lifetime rule as a child receipt: long enough for this job to queue (a
	// directory lock can wait an hour) and to run its own limit.
	ttl := s.config().WakeupTTL()
	if need := time.Duration(childWakeLimitSec(child, req)+childWakeQueueMarginSec) * time.Second; need > ttl {
		ttl = need
	}
	w := jobstore.WakeupRecord{
		ID:             "wk-" + RandomSuffix(),
		JobID:          target,
		Kind:           jobstore.WakeupKindEvent,
		EventTypesJSON: jobstore.EncodeStringList([]string{EventJobTerminal}),
		FilterJobID:    jobID,
		Mode:           jobstore.WakeupModeContinuous,
		Enabled:        1,
		Revision:       1,
		// auto_child marks the row as a receipt: it is spoken, not resumed, it follows
		// the work into a retry / takeover, and a gone session reports it once.
		AutoChild: 1,
		CreatedBy: req.CallerID,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
	}
	if err := s.meta.InsertWakeup(w); err != nil {
		return fmt.Errorf("register notify session receipt: %w", err)
	}
	slog.Info("wakeup.created", "event", "wakeup.created", "wakeup_id", w.ID,
		"job_id", w.JobID, "kind", w.Kind, "filter_job_id", w.FilterJobID, "auto_child", 1,
		"via", "notify_session")
	return nil
}

// dropNotifyTargetWatch undoes watchNotifyTarget on a submit that did not become
// a job. Best-effort by design: the row is anchored on a job id that no longer
// exists, so a leftover one can only cost a lookup.
func (s *Service) dropNotifyTargetWatch(req JobRequest, jobID string) {
	target := notifyTarget(req)
	if target == "" || target == strings.TrimSpace(req.SourceSessionID) {
		return
	}
	if notifyTargetJobID(target) {
		if _, isSession, err := s.meta.GetAgentSession(target); err != nil || !isSession {
			// Undo registerNotifySessionReceipt. Best-effort for the same reason as below.
			if rows, err := s.meta.ListEventWakeupsByChild(jobID); err == nil {
				for _, w := range rows {
					if w.JobID == target {
						_ = s.meta.DeleteWakeup(w.ID)
					}
				}
			}
			return
		}
	}
	_, _ = s.meta.RemoveSessionJobWatch(target, jobID)
}
