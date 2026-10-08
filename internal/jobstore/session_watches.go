package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
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

// ClaimUnwatchedSourceJobs registers only jobs whose source_session_id is the
// exact registered session and whose authenticated caller, project, canonical
// runner, and effective cwd still match that session. Caller identity alone is
// not enough to transfer a Stop watch between two sessions.
func (s *Store) ClaimUnwatchedSourceJobs(sessionID string, since int64) (int64, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("jobstore: begin source-session claim: %w", err)
	}
	defer tx.Rollback()
	var callerID, projectKey, runner, sessionCwd string
	err = tx.QueryRow(`SELECT COALESCE(caller_id,''), COALESCE(project_key,''), COALESCE(runner,''), COALESCE(cwd,'')
FROM agent_sessions WHERE session_id=?`, sessionID).Scan(&callerID, &projectKey, &runner, &sessionCwd)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("jobstore: load source session %q: %w", sessionID, err)
	}
	sessionCwd, sessionPathOK := canonicalExecutionCwd(sessionCwd)
	runner = config.NormalizeRunnerName(runner)
	if callerID == "" || projectKey == "" || runner == "" || !sessionPathOK {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(supervisedJobStatuses)), ",")
	args := make([]any, 0, util.CapSum(len(supervisedJobStatuses), 5))
	args = append(args, sessionID, callerID, projectKey, runner, since)
	for _, st := range supervisedJobStatuses {
		args = append(args, st)
	}
	rows, err := tx.Query(`SELECT j.id, COALESCE(j.cwd,'') FROM jobs j
WHERE j.source_session_id=? AND j.caller_id=? AND j.project_key=? AND j.runner=?
AND j.started_at >= ? AND j.status IN (`+placeholders+`)
AND NOT EXISTS (SELECT 1 FROM session_job_watches w WHERE w.job_id=j.id)
ORDER BY j.started_at ASC, j.id ASC`, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: query source-session jobs: %w", err)
	}
	type candidate struct{ id, cwd string }
	candidates := make([]candidate, 0)
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.cwd); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("jobstore: scan source-session job: %w", err)
		}
		jobCwd, ok := canonicalExecutionCwd(c.cwd)
		if ok && sameExecutionCwd(sessionCwd, jobCwd) {
			candidates = append(candidates, c)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("jobstore: read source-session jobs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("jobstore: close source-session jobs: %w", err)
	}
	createdAt := s.unixNow()
	var claimed int64
	for _, c := range candidates {
		res, err := tx.Exec(`INSERT INTO session_job_watches(session_id, job_id, created_at)
SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM session_job_watches WHERE job_id=?)
ON CONFLICT(session_id, job_id) DO NOTHING`, sessionID, c.id, createdAt, c.id)
		if err != nil {
			return 0, fmt.Errorf("jobstore: claim source job %q: %w", c.id, err)
		}
		n, _ := res.RowsAffected()
		claimed += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("jobstore: commit source-session claim: %w", err)
	}
	return claimed, nil
}

func canonicalExecutionCwd(cwd string) (string, bool) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || !filepath.IsAbs(cwd) {
		return "", false
	}
	return filepath.ToSlash(filepath.Clean(cwd)), true
}

func sameExecutionCwd(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
