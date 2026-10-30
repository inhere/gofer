package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// BackgroundSession is one reusable session of a server-owned background agent. The
// reuse key is kind + project + work_dir (+ agent and model), so kinds never share a
// session and a project/directory never sees another's context.
type BackgroundSession struct {
	Kind         string
	SessionID    string
	Agent        string
	Project      string
	WorkDir      string
	Model        string
	CreatedAt    int64
	LastUsedAt   int64
	Runs         int
	ContextUsed  int64
	ContextLimit int64
	LastUsage    string
	State        string
	ManualReset  bool
}

// ContextRatio is context_used / context_limit, 0 when the limit is unknown.
func (b BackgroundSession) ContextRatio() float64 {
	if b.ContextLimit <= 0 {
		return 0
	}
	return float64(b.ContextUsed) / float64(b.ContextLimit)
}

const backgroundSessionCols = `kind, session_id, agent, project, work_dir, model, created_at, last_used_at, runs,
  COALESCE(context_used,0), COALESCE(context_limit,0), COALESCE(last_usage_json,''), state, manual_reset`

// UpsertBackgroundSession inserts a session or replaces the mutable columns of the
// existing (kind, session_id) row; created_at of an existing row is kept.
func (s *Store) UpsertBackgroundSession(b BackgroundSession) error {
	if strings.TrimSpace(b.Kind) == "" || strings.TrimSpace(b.SessionID) == "" {
		return errors.New("jobstore: background session needs kind and session_id")
	}
	if b.State == "" {
		b.State = "active"
	}
	reset := 0
	if b.ManualReset {
		reset = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO background_agent_sessions
  (kind, session_id, agent, project, work_dir, model, created_at, last_used_at, runs, context_used, context_limit, last_usage_json, state, manual_reset)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(kind, session_id) DO UPDATE SET
    agent=excluded.agent, project=excluded.project, work_dir=excluded.work_dir, model=excluded.model,
    last_used_at=excluded.last_used_at, runs=excluded.runs, context_used=excluded.context_used,
    context_limit=excluded.context_limit, last_usage_json=excluded.last_usage_json,
    state=excluded.state, manual_reset=excluded.manual_reset`,
		b.Kind, b.SessionID, b.Agent, b.Project, b.WorkDir, b.Model, b.CreatedAt, b.LastUsedAt, b.Runs,
		b.ContextUsed, b.ContextLimit, b.LastUsage, b.State, reset)
	if err != nil {
		return fmt.Errorf("jobstore: upsert background session: %w", err)
	}
	return nil
}

// ActiveBackgroundSession returns the newest active, not manually reset session for
// the exact key (ok=false when there is none).
func (s *Store) ActiveBackgroundSession(kind, project, workDir, agent, model string) (BackgroundSession, bool, error) {
	var (
		b     BackgroundSession
		reset int
	)
	err := s.db.QueryRow(`SELECT `+backgroundSessionCols+` FROM background_agent_sessions
  WHERE kind=? AND project=? AND work_dir=? AND agent=? AND model=? AND state='active' AND manual_reset=0
  ORDER BY last_used_at DESC, created_at DESC LIMIT 1`, kind, project, workDir, agent, model).
		Scan(&b.Kind, &b.SessionID, &b.Agent, &b.Project, &b.WorkDir, &b.Model, &b.CreatedAt, &b.LastUsedAt, &b.Runs,
			&b.ContextUsed, &b.ContextLimit, &b.LastUsage, &b.State, &reset)
	if errors.Is(err, sql.ErrNoRows) {
		return BackgroundSession{}, false, nil
	}
	if err != nil {
		return BackgroundSession{}, false, fmt.Errorf("jobstore: read background session: %w", err)
	}
	b.ManualReset = reset != 0
	return b, true, nil
}

// ResetBackgroundSessions marks every active session of the key as manually reset so
// the next run starts a new one. It returns how many rows changed.
func (s *Store) ResetBackgroundSessions(kind, project, workDir string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE background_agent_sessions SET manual_reset=1, state='reset'
  WHERE kind=? AND project=? AND work_dir=? AND state='active'`, kind, project, workDir)
	if err != nil {
		return 0, fmt.Errorf("jobstore: reset background sessions: %w", err)
	}
	return res.RowsAffected()
}

// SetJobCheck stores the structured delivery-check outcome on a job. checkJSON is the
// job-check-result/v1 document, errText the last infrastructure error ("" = none).
func (s *Store) SetJobCheck(jobID, checkJSON string, resumeCount int, errText string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE jobs SET check_json=?, check_resume_count=?, check_error=? WHERE id=?`,
		checkJSON, resumeCount, errText, jobID)
	if err != nil {
		return fmt.Errorf("jobstore: set job check: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("jobstore: set job check: job %q not found", jobID)
	}
	return nil
}

// JobCheck reads the delivery-check columns of a job.
func (s *Store) JobCheck(jobID string) (checkJSON string, resumeCount int, errText string, err error) {
	err = s.db.QueryRow(`SELECT COALESCE(check_json,''), check_resume_count, COALESCE(check_error,'') FROM jobs WHERE id=?`, jobID).
		Scan(&checkJSON, &resumeCount, &errText)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, "", fmt.Errorf("jobstore: job %q not found", jobID)
	}
	return
}
