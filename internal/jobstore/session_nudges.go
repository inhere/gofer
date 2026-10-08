package jobstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Session nudges (N2 §E, SESS-12): a timer a person set on a terminal session. Kind
// "every" fires on a fixed period; kind "stalled" fires when the session made no
// progress for interval_sec. The firing itself (the stall check, the delivery) lives in
// sessionrelay; this file is only the durable row and its compare-and-set updates.

// Nudge kinds and states.
const (
	NudgeEvery   = "every"
	NudgeStalled = "stalled"

	NudgeActive = "active"
	NudgePaused = "paused"
	NudgeEnded  = "ended"
)

// Nudge ended reasons (ended_reason).
const (
	NudgeEndedUntil      = "until"
	NudgeEndedSessionEnd = "session_ended"
	NudgeEndedHandedOff  = "session_handed_off"
	NudgeEndedManual     = "removed"
)

// MaxNudgeText caps the stored nudge text.
const MaxNudgeText = 2000

// ErrNudgeInvalid marks a rejected nudge input (maps to 400); ErrNudgeNotFound to 404.
var (
	ErrNudgeInvalid  = errors.New("invalid session nudge")
	ErrNudgeNotFound = errors.New("session nudge not found")
)

// SessionNudge is one row of session_nudges. Timestamps are unix seconds.
type SessionNudge struct {
	ID          string
	SessionID   string
	Kind        string
	IntervalSec int64
	Text        string
	UntilAt     int64
	State       string
	PauseReason string
	EndedReason string
	CreatedBy   string
	CreatedAt   int64
	UpdatedAt   int64
	// NextRunAt is the next due instant of an "every" nudge (0 for "stalled").
	NextRunAt int64
	// LastFiredAt is the last delivery ATTEMPT (success or failure); a stalled nudge
	// measures its next threshold from it so a failing or answered nudge never repeats
	// faster than its own interval.
	LastFiredAt int64
	FireCount   int64
	// FailCount counts CONSECUTIVE failed deliveries; success resets it.
	FailCount int64
	LastError string
}

const selectNudgeCols = `SELECT id, session_id, kind, interval_sec, text, until_at, state, pause_reason,
  ended_reason, created_by, created_at, updated_at, next_run_at, last_fired_at, fire_count, fail_count, last_error
  FROM session_nudges`

func scanNudge(sc rowScanner) (SessionNudge, error) {
	var n SessionNudge
	err := sc.Scan(&n.ID, &n.SessionID, &n.Kind, &n.IntervalSec, &n.Text, &n.UntilAt, &n.State, &n.PauseReason,
		&n.EndedReason, &n.CreatedBy, &n.CreatedAt, &n.UpdatedAt, &n.NextRunAt, &n.LastFiredAt, &n.FireCount,
		&n.FailCount, &n.LastError)
	return n, err
}

func newNudgeID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "ng-" + hex.EncodeToString(b)
}

// CreateSessionNudge validates and inserts an active nudge. kind, interval_sec and text
// are required; an "every" nudge's first run is now+interval.
func (s *Store) CreateSessionNudge(n SessionNudge) (SessionNudge, error) {
	n.SessionID = strings.TrimSpace(n.SessionID)
	n.Text = strings.TrimSpace(n.Text)
	switch {
	case n.SessionID == "":
		return SessionNudge{}, fmt.Errorf("%w: session id required", ErrNudgeInvalid)
	case n.Kind != NudgeEvery && n.Kind != NudgeStalled:
		return SessionNudge{}, fmt.Errorf("%w: kind must be every or stalled", ErrNudgeInvalid)
	case n.IntervalSec < 60:
		return SessionNudge{}, fmt.Errorf("%w: interval must be at least 1m", ErrNudgeInvalid)
	case n.Text == "":
		return SessionNudge{}, fmt.Errorf("%w: message text required", ErrNudgeInvalid)
	case len([]rune(n.Text)) > MaxNudgeText:
		return SessionNudge{}, fmt.Errorf("%w: message text longer than %d characters", ErrNudgeInvalid, MaxNudgeText)
	}
	defer s.emit(Change{Kind: ChangeSession, ID: n.SessionID})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM agent_sessions WHERE session_id=?`, n.SessionID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionNudge{}, fmt.Errorf("jobstore: unknown agent session %q", n.SessionID)
		}
		return SessionNudge{}, fmt.Errorf("jobstore: check agent session %q: %w", n.SessionID, err)
	}
	now := s.unixNow()
	if n.UntilAt > 0 && n.UntilAt <= now {
		return SessionNudge{}, fmt.Errorf("%w: until is already in the past", ErrNudgeInvalid)
	}
	n.ID, n.State, n.CreatedAt, n.UpdatedAt = newNudgeID(), NudgeActive, now, now
	n.NextRunAt = 0
	if n.Kind == NudgeEvery {
		n.NextRunAt = now + n.IntervalSec
	}
	// A stalled nudge's clock starts at creation: it must not fire for a stall that
	// began before the person asked.
	n.LastFiredAt = 0
	if _, err := s.db.Exec(`INSERT INTO session_nudges
(id, session_id, kind, interval_sec, text, until_at, state, created_by, created_at, updated_at, next_run_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`, n.ID, n.SessionID, n.Kind, n.IntervalSec, n.Text, n.UntilAt, n.State,
		n.CreatedBy, n.CreatedAt, n.UpdatedAt, n.NextRunAt); err != nil {
		return SessionNudge{}, fmt.Errorf("jobstore: create session nudge: %w", err)
	}
	return n, nil
}

// GetSessionNudge reads one nudge.
func (s *Store) GetSessionNudge(id string) (SessionNudge, bool, error) {
	n, err := scanNudge(s.db.QueryRow(selectNudgeCols+" WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return SessionNudge{}, false, nil
	}
	if err != nil {
		return SessionNudge{}, false, fmt.Errorf("jobstore: get session nudge %q: %w", id, err)
	}
	return n, true, nil
}

// ListSessionNudges lists a session's nudges oldest first; ended ones only with
// includeEnded. An empty sid lists every session's.
func (s *Store) ListSessionNudges(sid string, includeEnded bool) ([]SessionNudge, error) {
	q := selectNudgeCols
	var where []string
	var args []any
	if sid != "" {
		where = append(where, "session_id=?")
		args = append(args, sid)
	}
	if !includeEnded {
		where = append(where, "state<>'ended'")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.Query(q+" ORDER BY created_at, id", args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list session nudges: %w", err)
	}
	defer rows.Close()
	var out []SessionNudge
	for rows.Next() {
		n, err := scanNudge(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan session nudge: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListActiveNudges is the sweeper's read: every active nudge of every session.
func (s *Store) ListActiveNudges() ([]SessionNudge, error) {
	rows, err := s.db.Query(selectNudgeCols + " WHERE state='active' ORDER BY created_at, id")
	if err != nil {
		return nil, fmt.Errorf("jobstore: list active nudges: %w", err)
	}
	defer rows.Close()
	var out []SessionNudge
	for rows.Next() {
		n, err := scanNudge(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan session nudge: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// EndSessionNudge ends an active or paused nudge for a reason (idempotent: false when
// it was already ended or unknown).
func (s *Store) EndSessionNudge(id, reason string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE session_nudges SET state='ended', ended_reason=?, updated_at=?, next_run_at=0
WHERE id=? AND state<>'ended'`, reason, s.unixNow(), id)
	if err != nil {
		return false, fmt.Errorf("jobstore: end session nudge %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.emit(Change{Kind: ChangeSession})
	}
	return n > 0, nil
}

// PauseSessionNudge pauses an active nudge (reason is kept for display).
func (s *Store) PauseSessionNudge(id, reason string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE session_nudges SET state='paused', pause_reason=?, updated_at=?
WHERE id=? AND state='active'`, reason, s.unixNow(), id)
	if err != nil {
		return false, fmt.Errorf("jobstore: pause session nudge %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.emit(Change{Kind: ChangeSession})
	}
	return n > 0, nil
}

// ResumeSessionNudge re-arms a paused nudge: the failure count and pause reason are
// cleared and the timers restart from now.
func (s *Store) ResumeSessionNudge(id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := s.unixNow()
	res, err := s.db.Exec(`UPDATE session_nudges SET state='active', pause_reason='', fail_count=0, last_error='',
  updated_at=?, last_fired_at=?, next_run_at=CASE WHEN kind='every' THEN ?+interval_sec ELSE 0 END
WHERE id=? AND state='paused'`, now, now, now, id)
	if err != nil {
		return false, fmt.Errorf("jobstore: resume session nudge %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.emit(Change{Kind: ChangeSession})
	}
	return n > 0, nil
}

// RecordNudgeAttempt stores the outcome of one delivery attempt of an ACTIVE nudge. A
// success resets the consecutive-failure count; a failure increments it and, on reaching
// maxFails, pauses the nudge (paused=true). nextRun re-arms an "every" nudge (ignored for
// "stalled"). A nudge that is no longer active (paused / ended meanwhile) is left alone.
func (s *Store) RecordNudgeAttempt(id string, now int64, ok bool, errText string, nextRun int64, maxFails int) (n SessionNudge, paused bool, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cur, err := scanNudge(s.db.QueryRow(selectNudgeCols+" WHERE id=?", id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionNudge{}, false, ErrNudgeNotFound
		}
		return SessionNudge{}, false, fmt.Errorf("jobstore: read session nudge %q: %w", id, err)
	}
	if cur.State != NudgeActive {
		return cur, false, nil
	}
	cur.LastFiredAt, cur.UpdatedAt = now, now
	if cur.Kind == NudgeEvery {
		cur.NextRunAt = nextRun
	}
	if ok {
		cur.FireCount++
		cur.FailCount, cur.LastError = 0, ""
	} else {
		cur.FailCount++
		cur.LastError = oneLineCap(errText, 500)
		if maxFails > 0 && cur.FailCount >= int64(maxFails) {
			cur.State, cur.PauseReason, paused = NudgePaused, fmt.Sprintf("%d consecutive delivery failures", cur.FailCount), true
		}
	}
	if _, err := s.db.Exec(`UPDATE session_nudges SET state=?, pause_reason=?, updated_at=?, next_run_at=?,
  last_fired_at=?, fire_count=?, fail_count=?, last_error=? WHERE id=?`, cur.State, cur.PauseReason, cur.UpdatedAt,
		cur.NextRunAt, cur.LastFiredAt, cur.FireCount, cur.FailCount, cur.LastError, id); err != nil {
		return SessionNudge{}, false, fmt.Errorf("jobstore: record nudge attempt %q: %w", id, err)
	}
	s.emit(Change{Kind: ChangeSession, ID: cur.SessionID})
	return cur, paused, nil
}

// DeleteSessionNudge removes a nudge row (false when unknown).
func (s *Store) DeleteSessionNudge(id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM session_nudges WHERE id=?`, id)
	if err != nil {
		return false, fmt.Errorf("jobstore: delete session nudge %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.emit(Change{Kind: ChangeSession})
	}
	return n > 0, nil
}

func oneLineCap(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
