package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// SummarizerSession is the work summarizer's per-project session book (gofer-ayu0).
// Instead of opening a new agent session for every tidy-up, the summarizer keeps ONE
// session per project and continues it, rotating to a fresh one once it has served
// Runs tidy-ups or is older than the configured age. The record is what survives a
// server restart: SessionID is the agent-side id the runs are pinned to, LastJobID the
// job whose session can be continued ("" = the next run must start a new session).
type SummarizerSession struct {
	ProjectKey string
	SessionID  string
	LastJobID  string
	Runs       int
	CreatedAt  int64
	UpdatedAt  int64
}

// GetSummarizerSession reads a project's summarizer session (false = never recorded).
func (s *Store) GetSummarizerSession(project string) (SummarizerSession, bool, error) {
	row := s.db.QueryRow(`SELECT project_key, session_id, last_job_id, runs, created_at, updated_at
  FROM summarizer_sessions WHERE project_key = ?`, project)
	var r SummarizerSession
	err := row.Scan(&r.ProjectKey, &r.SessionID, &r.LastJobID, &r.Runs, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SummarizerSession{}, false, nil
	}
	if err != nil {
		return SummarizerSession{}, false, fmt.Errorf("jobstore: get summarizer session: %w", err)
	}
	return r, true, nil
}

// SaveSummarizerSession writes (or replaces) the record of a project's summarizer
// session. An empty ProjectKey is refused: there is no meaningful global record.
func (s *Store) SaveSummarizerSession(r SummarizerSession) error {
	if r.ProjectKey == "" {
		return errors.New("jobstore: save summarizer session: an empty project key")
	}
	if _, err := s.db.Exec(`INSERT INTO summarizer_sessions
  (project_key, session_id, last_job_id, runs, created_at, updated_at)
  VALUES (?, ?, ?, ?, ?, ?)
  ON CONFLICT(project_key) DO UPDATE SET session_id = excluded.session_id,
    last_job_id = excluded.last_job_id, runs = excluded.runs,
    created_at = excluded.created_at, updated_at = excluded.updated_at`,
		r.ProjectKey, r.SessionID, r.LastJobID, r.Runs, r.CreatedAt, r.UpdatedAt); err != nil {
		return fmt.Errorf("jobstore: save summarizer session: %w", err)
	}
	return nil
}

// DeleteSummarizerSession forgets a project's summarizer session, so the next tidy-up
// opens a fresh one.
func (s *Store) DeleteSummarizerSession(project string) error {
	if _, err := s.db.Exec(`DELETE FROM summarizer_sessions WHERE project_key = ?`, project); err != nil {
		return fmt.Errorf("jobstore: delete summarizer session: %w", err)
	}
	return nil
}

// ListSummarizerSessionIDs returns every session id the summarizer currently holds
// (the ones a session list must hide while they belong to a tidy-up).
func (s *Store) ListSummarizerSessionIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT session_id FROM summarizer_sessions WHERE session_id <> ''`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list summarizer session ids: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("jobstore: scan summarizer session id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list summarizer session ids rows: %w", err)
	}
	return out, nil
}
