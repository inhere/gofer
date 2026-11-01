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
	// LastJobID is the job of the newest turn: the one a continuation resumes (the
	// summarizer's session book, the steward's idle-end resume).
	LastJobID string
	// RotationReason is why this session replaced its predecessor ("" = it did not).
	RotationReason string
}

// ContextRatio is context_used / context_limit, 0 when the limit is unknown.
func (b BackgroundSession) ContextRatio() float64 {
	if b.ContextLimit <= 0 {
		return 0
	}
	return float64(b.ContextUsed) / float64(b.ContextLimit)
}

const backgroundSessionCols = `kind, session_id, agent, project, work_dir, model, created_at, last_used_at, runs,
  COALESCE(context_used,0), COALESCE(context_limit,0), COALESCE(last_usage_json,''), state, manual_reset,
  last_job_id, rotation_reason`

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
  (kind, session_id, agent, project, work_dir, model, created_at, last_used_at, runs, context_used, context_limit, last_usage_json, state, manual_reset, last_job_id, rotation_reason)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(kind, session_id) DO UPDATE SET
    agent=excluded.agent, project=excluded.project, work_dir=excluded.work_dir, model=excluded.model,
    last_used_at=excluded.last_used_at, runs=excluded.runs, context_used=excluded.context_used,
    context_limit=excluded.context_limit, last_usage_json=excluded.last_usage_json,
    state=excluded.state, manual_reset=excluded.manual_reset, last_job_id=excluded.last_job_id,
    rotation_reason=excluded.rotation_reason`,
		b.Kind, b.SessionID, b.Agent, b.Project, b.WorkDir, b.Model, b.CreatedAt, b.LastUsedAt, b.Runs,
		nullableCtx(b.ContextUsed), nullableCtx(b.ContextLimit), b.LastUsage, b.State, reset,
		b.LastJobID, b.RotationReason)
	if err != nil {
		return fmt.Errorf("jobstore: upsert background session: %w", err)
	}
	return nil
}

// scanBackgroundSession reads one backgroundSessionCols row.
func scanBackgroundSession(row interface{ Scan(...any) error }) (BackgroundSession, error) {
	var (
		b     BackgroundSession
		reset int
	)
	err := row.Scan(&b.Kind, &b.SessionID, &b.Agent, &b.Project, &b.WorkDir, &b.Model, &b.CreatedAt, &b.LastUsedAt, &b.Runs,
		&b.ContextUsed, &b.ContextLimit, &b.LastUsage, &b.State, &reset, &b.LastJobID, &b.RotationReason)
	b.ManualReset = reset != 0
	return b, err
}

// nullableCtx stores an unknown context figure (0) as NULL, so "unknown" stays
// distinguishable from a real value in the table. The readers COALESCE it back to 0 and
// treat a 0 limit as unknown.
func nullableCtx(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

// ActiveBackgroundSession returns the newest active, not manually reset session for
// the exact key (ok=false when there is none).
func (s *Store) ActiveBackgroundSession(kind, project, workDir, agent, model string) (BackgroundSession, bool, error) {
	return s.oneBackgroundSession(`WHERE kind=? AND project=? AND work_dir=? AND agent=? AND model=? AND state='active' AND manual_reset=0`,
		kind, project, workDir, agent, model)
}

// LatestBackgroundSession returns the newest session row of the key in ANY state, so a
// caller can tell "never had one" from "the operator reset it".
func (s *Store) LatestBackgroundSession(kind, project, workDir, agent, model string) (BackgroundSession, bool, error) {
	return s.oneBackgroundSession(`WHERE kind=? AND project=? AND work_dir=? AND agent=? AND model=?`,
		kind, project, workDir, agent, model)
}

// ActiveUnboundBackgroundSession returns the newest active session of kind + project +
// work_dir that was recorded without an agent and model (agent=” AND model=”): the rows
// the one-time import of the older per-kind session books wrote. The runner adopts such a
// row for the first run that asks, binding it to that run's agent and model.
func (s *Store) ActiveUnboundBackgroundSession(kind, project, workDir string) (BackgroundSession, bool, error) {
	return s.oneBackgroundSession(`WHERE kind=? AND project=? AND work_dir=? AND agent='' AND model='' AND state='active' AND manual_reset=0`,
		kind, project, workDir)
}

func (s *Store) oneBackgroundSession(where string, args ...any) (BackgroundSession, bool, error) {
	b, err := scanBackgroundSession(s.db.QueryRow(`SELECT `+backgroundSessionCols+` FROM background_agent_sessions `+where+`
  ORDER BY last_used_at DESC, created_at DESC, rowid DESC LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return BackgroundSession{}, false, nil
	}
	if err != nil {
		return BackgroundSession{}, false, fmt.Errorf("jobstore: read background session: %w", err)
	}
	return b, true, nil
}

// ListBackgroundSessions returns the active sessions of a kind, newest first (the status
// view of a kind that keeps one session per project / work_dir).
func (s *Store) ListBackgroundSessions(kind string) ([]BackgroundSession, error) {
	rows, err := s.db.Query(`SELECT `+backgroundSessionCols+` FROM background_agent_sessions
  WHERE kind=? AND state='active' AND manual_reset=0 ORDER BY last_used_at DESC, created_at DESC, rowid DESC`, kind)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list background sessions: %w", err)
	}
	defer rows.Close()
	var out []BackgroundSession
	for rows.Next() {
		b, err := scanBackgroundSession(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan background session: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ReplaceBackgroundSession writes b as the session of its kind + project + work_dir and
// retires every other active row of that scope (state 'replaced'), in one transaction: the
// session id can change between turns (the agent reports a new one, a rotation opens a
// fresh one), the agent / model of the kind can change, and a scope with two active rows
// would make the next lookup ambiguous. Rows imported without agent / model are retired
// the same way.
func (s *Store) ReplaceBackgroundSession(b BackgroundSession) error {
	if strings.TrimSpace(b.Kind) == "" || strings.TrimSpace(b.SessionID) == "" {
		return errors.New("jobstore: background session needs kind and session_id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: replace background session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE background_agent_sessions SET state='replaced'
  WHERE kind=? AND project=? AND work_dir=? AND state='active' AND session_id<>?`,
		b.Kind, b.Project, b.WorkDir, b.SessionID); err != nil {
		return fmt.Errorf("jobstore: retire replaced background sessions: %w", err)
	}
	if b.State == "" {
		b.State = "active"
	}
	reset := 0
	if b.ManualReset {
		reset = 1
	}
	if _, err := tx.Exec(`INSERT INTO background_agent_sessions
  (kind, session_id, agent, project, work_dir, model, created_at, last_used_at, runs, context_used, context_limit, last_usage_json, state, manual_reset, last_job_id, rotation_reason)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(kind, session_id) DO UPDATE SET
    agent=excluded.agent, project=excluded.project, work_dir=excluded.work_dir, model=excluded.model,
    created_at=excluded.created_at, last_used_at=excluded.last_used_at, runs=excluded.runs,
    context_used=excluded.context_used, context_limit=excluded.context_limit,
    last_usage_json=excluded.last_usage_json, state=excluded.state, manual_reset=excluded.manual_reset,
    last_job_id=excluded.last_job_id, rotation_reason=excluded.rotation_reason`,
		b.Kind, b.SessionID, b.Agent, b.Project, b.WorkDir, b.Model, b.CreatedAt, b.LastUsedAt, b.Runs,
		nullableCtx(b.ContextUsed), nullableCtx(b.ContextLimit), b.LastUsage, b.State, reset,
		b.LastJobID, b.RotationReason); err != nil {
		return fmt.Errorf("jobstore: write background session: %w", err)
	}
	return tx.Commit()
}

// RetireBackgroundSession ends one session (state e.g. 'dropped' for a continuation that
// failed); the row stays for the history.
func (s *Store) RetireBackgroundSession(kind, sessionID, state string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE background_agent_sessions SET state=? WHERE kind=? AND session_id=?`, state, kind, sessionID); err != nil {
		return fmt.Errorf("jobstore: retire background session: %w", err)
	}
	return nil
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
