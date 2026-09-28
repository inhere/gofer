package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SessionJobWatch is the short-lived relation between a terminal session and
// a job that session asked gofer to watch. Job state remains in jobs; this row
// only records the reference and registration time.
type SessionJobWatch struct {
	SessionID string
	JobID     string
	CreatedAt int64
}

// AddSessionJobWatch records a watch idempotently. The session must already
// exist; callers decide whether the referenced job is visible to them.
func (s *Store) AddSessionJobWatch(sessionID, jobID string) (SessionJobWatch, error) {
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if sessionID == "" || jobID == "" {
		return SessionJobWatch{}, errors.New("jobstore: session watch requires session_id and job_id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM agent_sessions WHERE session_id=?`, sessionID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionJobWatch{}, fmt.Errorf("jobstore: unknown agent session %q", sessionID)
		}
		return SessionJobWatch{}, fmt.Errorf("jobstore: check agent session %q: %w", sessionID, err)
	}
	now := s.unixNow()
	_, err := s.db.Exec(`INSERT INTO session_job_watches(session_id, job_id, created_at)
VALUES (?, ?, ?) ON CONFLICT(session_id, job_id) DO NOTHING`, sessionID, jobID, now)
	if err != nil {
		return SessionJobWatch{}, fmt.Errorf("jobstore: add session watch %q/%q: %w", sessionID, jobID, err)
	}
	var out SessionJobWatch
	err = s.db.QueryRow(`SELECT session_id, job_id, created_at FROM session_job_watches WHERE session_id=? AND job_id=?`, sessionID, jobID).
		Scan(&out.SessionID, &out.JobID, &out.CreatedAt)
	if err != nil {
		return SessionJobWatch{}, fmt.Errorf("jobstore: read session watch %q/%q: %w", sessionID, jobID, err)
	}
	return out, nil
}

// ListSessionJobWatches returns watches in registration order.
func (s *Store) ListSessionJobWatches(sessionID string) ([]SessionJobWatch, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("jobstore: session watch list requires session_id")
	}
	rows, err := s.db.Query(`SELECT session_id, job_id, created_at
FROM session_job_watches WHERE session_id=? ORDER BY created_at ASC, job_id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list session watches %q: %w", sessionID, err)
	}
	defer rows.Close()
	out := make([]SessionJobWatch, 0)
	for rows.Next() {
		var w SessionJobWatch
		if err := rows.Scan(&w.SessionID, &w.JobID, &w.CreatedAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan session watch: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list session watches rows: %w", err)
	}
	return out, nil
}

// RemoveSessionJobWatch removes one watch and reports whether it existed.
func (s *Store) RemoveSessionJobWatch(sessionID, jobID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM session_job_watches WHERE session_id=? AND job_id=?`, sessionID, jobID)
	if err != nil {
		return false, fmt.Errorf("jobstore: remove session watch %q/%q: %w", sessionID, jobID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClearSessionJobWatches removes all watches for a session. It is idempotent.
func (s *Store) ClearSessionJobWatches(sessionID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM session_job_watches WHERE session_id=?`, sessionID); err != nil {
		return fmt.Errorf("jobstore: clear session watches %q: %w", sessionID, err)
	}
	return nil
}
