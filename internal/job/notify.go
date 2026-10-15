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
	"fmt"
	"strings"
)

// notifyTargetKinds are the two accepted spellings of "enable the notification":
// the flag/field itself, or an explicit target session (which implies it).
func notifyRequested(req JobRequest) bool {
	return req.Notify || strings.TrimSpace(req.NotifySession) != ""
}

// notifyTarget is the session a finished job tells: the explicit NotifySession
// when given, else the job's authenticated source session. "" means nobody — a
// request in that state is refused at submit (see validateNotifyRequest).
func notifyTarget(req JobRequest) string {
	if !notifyRequested(req) {
		return ""
	}
	if sid := strings.TrimSpace(req.NotifySession); sid != "" {
		return sid
	}
	return strings.TrimSpace(req.SourceSessionID)
}

// validateNotifyRequest refuses an end-notification gofer cannot deliver:
//
//   - `--notify` with no session at all (neither an explicit target nor a source
//     session). Silently doing nothing would hide the mistake, and guessing a
//     session would send the notice somewhere the caller never named.
//   - an explicit target that is not a registered session of THIS server: the
//     notice has no address to go to, and (like a foreign source session) it is
//     refused rather than accepted-and-lost.
//
// It runs with — and after — validateSourceSession, so a target that IS the
// source session has already been proven to belong to the authenticated caller.
// An explicit target owned by someone else is refused for the same reason a
// source session is: telling another caller's terminal about your job is the
// same cross-caller write.
func (s *Service) validateNotifyRequest(req JobRequest) error {
	if !notifyRequested(req) {
		return nil
	}
	target := notifyTarget(req)
	if target == "" {
		return fmt.Errorf("%w: notify needs a session to tell: pass --notify-session <sid> (or notify_session), set the source session, or run it from the agent session itself", ErrInvalidRequest)
	}
	if target == strings.TrimSpace(req.SourceSessionID) {
		return nil // proven by validateSourceSession
	}
	session, ok, err := s.meta.GetAgentSession(target)
	if err != nil {
		return fmt.Errorf("load notify session: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: notify session %q is not a registered session of this server", ErrInvalidRequest, target)
	}
	caller := strings.TrimSpace(req.CallerID)
	if session.CallerID != "" && caller != "" && session.CallerID != caller {
		return fmt.Errorf("%w: notify session %q belongs to another caller", ErrInvalidRequest, target)
	}
	return nil
}

// watchNotifyTarget registers the notify target's pending record, so the terminal
// notify has something durable to claim. It is a no-op when the target is the
// source session (Submit has already added that watch) or when notify is off.
func (s *Service) watchNotifyTarget(req JobRequest, jobID string) error {
	target := notifyTarget(req)
	if target == "" || target == strings.TrimSpace(req.SourceSessionID) {
		return nil
	}
	if _, err := s.meta.AddSessionJobWatch(target, jobID); err != nil {
		return fmt.Errorf("register notify session watch: %w", err)
	}
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
	_, _ = s.meta.RemoveSessionJobWatch(target, jobID)
}
