package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Suggestion fields the summarizer may propose (W2a, design §14.2). The first five
// are descriptive fields; status_hint is only ever a hint, never applied to the status.
const (
	SuggestGoal        = "goal"
	SuggestBlocker     = "blocker"
	SuggestBlockerKind = "blocker_kind"
	SuggestNext        = "next"
	SuggestSummary     = "summary"
	SuggestStatusHint  = "status_hint"
)

// Suggestion states.
const (
	SuggestionPending   = "pending"
	SuggestionDismissed = "dismissed"
)

// ErrSuggestionNotFound is returned when there is no pending suggestion for a field.
var ErrSuggestionNotFound = errors.New("work suggestion not found")

// ValidSuggestionField reports whether f is a suggestion field.
func ValidSuggestionField(f string) bool {
	switch f {
	case SuggestGoal, SuggestBlocker, SuggestBlockerKind, SuggestNext, SuggestSummary, SuggestStatusHint:
		return true
	}
	return false
}

// WorkSuggestion is a proposal the summarizer made for a field it was not allowed to
// overwrite (a person or a session wrote it). The person adopts or dismisses it.
type WorkSuggestion struct {
	WorkItemID string  `json:"work_item_id,omitempty"`
	Field      string  `json:"field"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence,omitempty"`
	By         string  `json:"by"`
	At         int64   `json:"at"`
	State      string  `json:"state"`
	JobID      string  `json:"job_id,omitempty"`
}

// UpsertWorkSuggestion stores (or replaces) the pending suggestion for a field. A
// proposal identical to one the person already dismissed is not stored again
// (stored=false), so a dismissed idea does not keep coming back.
func (s *Store) UpsertWorkSuggestion(sg WorkSuggestion) (stored bool, err error) {
	id := strings.TrimSpace(sg.WorkItemID)
	if !ValidSuggestionField(sg.Field) {
		return false, fmt.Errorf("%w: invalid suggestion field %q", ErrWorkInvalid, sg.Field)
	}
	val := capText(sg.Value, maxWorkField)
	if val == "" {
		return false, nil
	}
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var oldVal, oldState string
	qerr := s.db.QueryRow(`SELECT value, state FROM work_suggestions WHERE work_item_id = ? AND field = ?`, id, sg.Field).Scan(&oldVal, &oldState)
	if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return false, fmt.Errorf("jobstore: read work suggestion: %w", qerr)
	}
	if qerr == nil && oldState == SuggestionDismissed && oldVal == val {
		return false, nil
	}
	now := s.unixNow()
	if sg.At > 0 {
		now = sg.At
	}
	if _, err := s.db.Exec(`INSERT INTO work_suggestions(work_item_id, field, value, confidence, by, at, state, job_id)
  VALUES (?,?,?,?,?,?,'pending',?) ON CONFLICT(work_item_id, field) DO UPDATE SET value=excluded.value,
  confidence=excluded.confidence, by=excluded.by, at=excluded.at, state='pending', job_id=excluded.job_id`,
		id, sg.Field, val, sg.Confidence, sg.By, now, sg.JobID); err != nil {
		return false, fmt.Errorf("jobstore: upsert work suggestion: %w", err)
	}
	return true, nil
}

// GetWorkSuggestion reads one pending suggestion.
func (s *Store) GetWorkSuggestion(id, field string) (WorkSuggestion, bool, error) {
	var sg WorkSuggestion
	err := s.db.QueryRow(`SELECT work_item_id, field, value, confidence, by, at, state, job_id FROM work_suggestions
  WHERE work_item_id = ? AND field = ? AND state = 'pending'`, strings.TrimSpace(id), field).
		Scan(&sg.WorkItemID, &sg.Field, &sg.Value, &sg.Confidence, &sg.By, &sg.At, &sg.State, &sg.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkSuggestion{}, false, nil
	}
	return sg, err == nil, err
}

// ListWorkSuggestions returns an item's pending suggestions.
func (s *Store) ListWorkSuggestions(id string) ([]WorkSuggestion, error) {
	rows, err := s.db.Query(`SELECT field, value, confidence, by, at, state, job_id FROM work_suggestions
  WHERE work_item_id = ? AND state = 'pending' ORDER BY field`, strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("jobstore: list work suggestions: %w", err)
	}
	defer rows.Close()
	out := make([]WorkSuggestion, 0)
	for rows.Next() {
		var sg WorkSuggestion
		if err := rows.Scan(&sg.Field, &sg.Value, &sg.Confidence, &sg.By, &sg.At, &sg.State, &sg.JobID); err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// DismissWorkSuggestion marks a pending suggestion dismissed (kept so the same idea is
// not proposed again).
func (s *Store) DismissWorkSuggestion(id, field string) error {
	id = strings.TrimSpace(id)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE work_suggestions SET state = 'dismissed' WHERE work_item_id = ? AND field = ? AND state = 'pending'`, id, field)
	if err != nil {
		return fmt.Errorf("jobstore: dismiss work suggestion: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSuggestionNotFound
	}
	return nil
}

// DeleteWorkSuggestion removes a suggestion row outright (adopted, or superseded by a
// direct write).
func (s *Store) DeleteWorkSuggestion(id, field string) error {
	id = strings.TrimSpace(id)
	defer s.emit(Change{Kind: ChangeWork, ID: id})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`DELETE FROM work_suggestions WHERE work_item_id = ? AND field = ?`, id, field)
	return err
}

// ---------------------------------------------------------------- summarizer run log

// Summary run states.
const (
	SummaryRunning = "running"
	SummaryOK      = "ok"
	SummaryFailed  = "failed"
)

// WorkSummaryRun is one tidy-up attempt: it is the cost-control ledger (per-session
// interval, daily cap, "was there new activity since") and an audit trail.
type WorkSummaryRun struct {
	ID         int64  `json:"id"`
	WorkItemID string `json:"work_item_id"`
	SessionID  string `json:"session_id,omitempty"`
	At         int64  `json:"at"`
	ActivityAt int64  `json:"activity_at,omitempty"`
	State      string `json:"state"`
	JobID      string `json:"job_id,omitempty"`
	Cause      string `json:"cause,omitempty"`
	Error      string `json:"error,omitempty"`
}

// BeginWorkSummary records a tidy-up that is starting. activityAt is the session's
// last-seen time the run covers.
func (s *Store) BeginWorkSummary(itemID, sessionID string, activityAt int64, cause string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO work_summaries(work_item_id, session_id, at, activity_at, state, cause) VALUES (?,?,?,?,?,?)`,
		strings.TrimSpace(itemID), sessionID, s.unixNow(), activityAt, SummaryRunning, cause)
	if err != nil {
		return 0, fmt.Errorf("jobstore: begin work summary: %w", err)
	}
	return res.LastInsertId()
}

// FinishWorkSummary settles a run.
func (s *Store) FinishWorkSummary(id int64, state, jobID, errText string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE work_summaries SET state = ?, job_id = ?, error = ? WHERE id = ?`, state, jobID, capText(errText, 1000), id)
	return err
}

// FailStaleWorkSummaries settles runs left `running` before cutoff (the process died
// under them).
func (s *Store) FailStaleWorkSummaries(cutoff int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE work_summaries SET state = 'failed', error = 'interrupted' WHERE state = 'running' AND at < ?`, cutoff)
	return err
}

// LastWorkSummary returns the most recent run for a session.
func (s *Store) LastWorkSummary(sessionID string) (WorkSummaryRun, bool, error) {
	var r WorkSummaryRun
	err := s.db.QueryRow(`SELECT id, work_item_id, session_id, at, activity_at, state, job_id, cause, error FROM work_summaries
  WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID).
		Scan(&r.ID, &r.WorkItemID, &r.SessionID, &r.At, &r.ActivityAt, &r.State, &r.JobID, &r.Cause, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkSummaryRun{}, false, nil
	}
	return r, err == nil, err
}

// CountAutoWorkSummaries counts the runs started since `since` that were not a manual
// request (the daily cap applies to those).
func (s *Store) CountAutoWorkSummaries(since int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM work_summaries WHERE at >= ? AND cause <> 'manual'`, since).Scan(&n)
	return n, err
}

// ListWorkSummaries returns an item's recent runs, newest first.
func (s *Store) ListWorkSummaries(itemID string, limit int) ([]WorkSummaryRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, work_item_id, session_id, at, activity_at, state, job_id, cause, error FROM work_summaries
  WHERE work_item_id = ? ORDER BY id DESC LIMIT ?`, strings.TrimSpace(itemID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkSummaryRun, 0)
	for rows.Next() {
		var r WorkSummaryRun
		if err := rows.Scan(&r.ID, &r.WorkItemID, &r.SessionID, &r.At, &r.ActivityAt, &r.State, &r.JobID, &r.Cause, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
