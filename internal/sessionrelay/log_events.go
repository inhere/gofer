package sessionrelay

import (
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The relay's own log events, so a session's story can be read from the server log without
// reading the store:
//
//	session.state_changed  a hook event (or a relay action) moved the session's state
//	session.deliver        one delivery attempt's outcome: which transport carried the text,
//	                       which ones were abandoned on the way and why, the job behind it
//
// The ladder is the part that used to be invisible: a delivery that fell from `command` to
// `tmux` to `takeover` left nothing behind, and "why did it not reach my terminal" needed the
// code path. SR1301: ids, paths, reason codes and job ids only — never the delivered text.
const (
	// EventStateChanged is one session state transition.
	EventStateChanged = "session.state_changed"
	// EventDeliver is one delivery attempt's result.
	EventDeliver = "session.deliver"
)

// maxTrackedStates bounds the state memory (one entry per session this process has seen).
const maxTrackedStates = 4096

// stateStamp is the last state this process observed for a session.
type stateStamp struct {
	state string
	at    time.Time
}

// noteStateChange reports a session state transition (session / from / to / event). The
// previous state is remembered in memory instead of being re-read on every hook beat: the
// relay does not add a store read to the hot hook path just to phrase a log line. A session
// this process has not seen yet is only remembered, so nothing is claimed about a state we
// did not observe.
func (s *Service) noteStateChange(a jobstore.AgentSession, event string) {
	s.noteState(a.SessionID, a.State, event)
}

// noteState is noteStateChange for the relay's own transitions, where only the id and the new
// state are at hand (a wait released, a takeover started, a Stop take closed a turn).
func (s *Service) noteState(sid, state, event string) {
	if sid == "" || state == "" {
		return
	}
	now := s.nowFn()
	s.stateMu.Lock()
	if s.stateSeen == nil {
		s.stateSeen = make(map[string]stateStamp)
	}
	prev, seen := s.stateSeen[sid]
	if _, ok := s.stateSeen[sid]; !ok && len(s.stateSeen) >= maxTrackedStates {
		s.evictOldestStateLocked()
	}
	s.stateSeen[sid] = stateStamp{state: state, at: now}
	s.stateMu.Unlock()
	if !seen || prev.state == state {
		return
	}
	slog.Info(EventStateChanged, "session", sid, "from", prev.state, "to", state, "event", event)
}

// evictOldestStateLocked drops the longest-untouched session (s.stateMu held).
func (s *Service) evictOldestStateLocked() {
	oldestKey := ""
	var oldest time.Time
	for k, v := range s.stateSeen {
		if oldestKey == "" || v.at.Before(oldest) {
			oldestKey, oldest = k, v.at
		}
	}
	if oldestKey != "" {
		delete(s.stateSeen, oldestKey)
	}
}

// forgetState drops a session from the state memory (its row is gone / ended for good).
func (s *Service) forgetState(sid string) {
	s.stateMu.Lock()
	delete(s.stateSeen, sid)
	s.stateMu.Unlock()
}

// logDeliver reports one delivery attempt: where the text went (or why it did not), the paths
// the ladder abandoned first, and the job that carried it. A held message is a success of its
// own kind — it waits in the mailbox for the session's next hook.
func (s *Service) logDeliver(sid, by string, res DeliverResult, err error, skipped []string) {
	path := res.Path
	outcome := "ok"
	reason := ""
	if err != nil {
		outcome, reason = "failed", DeliverReason(err)
		if reason == "" {
			reason = "error"
		}
	}
	attrs := []any{"session", sid, "by", by, "path", path, "outcome", outcome}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	if res.JobID != "" {
		attrs = append(attrs, "job_id", res.JobID)
	}
	if res.Delivering {
		attrs = append(attrs, "delivering", true)
	}
	if res.MailID > 0 {
		attrs = append(attrs, "mail_id", res.MailID)
	}
	if len(skipped) > 0 {
		attrs = append(attrs, "skipped", strings.Join(skipped, ","))
	}
	if err != nil {
		slog.Warn(EventDeliver, attrs...)
		return
	}
	slog.Info(EventDeliver, attrs...)
}

// EventMailClaimReleased is the mailbox event name of a letter handed back to pending
// (internal/mail owns the mailbox events; the relay only reuses the name for the hook's own
// give-backs, which that package cannot see).
const EventMailClaimReleased = "mail.claim_released"

// logMailClaimsReleased reports the letters a hook took and gave back, so a letter that keeps
// bouncing between claim and release is visible instead of silently waiting.
func (s *Service) logMailClaimsReleased(sid string, ids []int64, reason string) {
	for _, id := range ids {
		slog.Info(EventMailClaimReleased, "mail_id", id, "session", sid, "reason", reason)
	}
}

// logRaw reports one raw (mailbox) delivery: the wake prompt or a letter's body sent through
// the live-process channel. path says which transport ran, reason why none did.
func (s *Service) logRaw(sid, by string, res RawResult, err error) {
	attrs := []any{"session", sid, "by", by, "path", res.Path, "outcome", "ok"}
	if err != nil {
		reason := DeliverReason(err)
		if reason == "" {
			reason = "error"
		}
		attrs = []any{"session", sid, "by", by, "path", res.Path, "outcome", "failed", "reason", reason}
	}
	if res.JobID != "" {
		attrs = append(attrs, "job_id", res.JobID)
	}
	if err != nil {
		slog.Warn(EventDeliver, attrs...)
		return
	}
	slog.Info(EventDeliver, attrs...)
}
