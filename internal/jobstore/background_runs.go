package jobstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Background run statuses (background_agent_runs.status).
const (
	BackgroundRunOK        = "ok"
	BackgroundRunFailed    = "failed"
	BackgroundRunCanceled  = "canceled"
	BackgroundRunBlocked   = "blocked" // a hard stop refused the run before it started
	BackgroundRunTimedOut  = "timeout"
	AuditBackgroundRotated = "background_agent.rotated"
	AuditBackgroundStopped = "background_agent.hard_stop"
)

// BackgroundRun is one row of background_agent_runs: the metadata of one run of a
// background agent. The prompt and the output are NOT stored.
type BackgroundRun struct {
	ID             int64
	Kind           string
	Project        string
	JobID          string
	SessionID      string
	StartedAt      int64
	EndedAt        int64
	Status         string
	Mode           string
	RotationReason string
	InputBytes     int64
	OutputBytes    int64
	UsageJSON      string
	Error          string
}

// InsertBackgroundRun records one run and returns its row id.
func (s *Store) InsertBackgroundRun(r BackgroundRun) (int64, error) {
	if strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Status) == "" {
		return 0, errors.New("jobstore: background run needs kind and status")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO background_agent_runs
  (kind, job_id, session_id, started_at, ended_at, status, input_bytes, output_bytes, usage_json, error, project, mode, rotation_reason)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Kind, r.JobID, r.SessionID, r.StartedAt, r.EndedAt, r.Status, r.InputBytes, r.OutputBytes,
		nullableText(r.UsageJSON), nullableText(r.Error), r.Project, r.Mode, r.RotationReason)
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert background run: %w", err)
	}
	return res.LastInsertId()
}

// SetBackgroundRunUsage fills the usage of the run of jobID once the job has ended and
// reported it (a resident session's turn only knows its usage afterwards). It only writes
// a run that has none yet and reports whether a row changed.
func (s *Store) SetBackgroundRunUsage(kind, jobID, usageJSON string) (bool, error) {
	if kind == "" || jobID == "" || usageJSON == "" {
		return false, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE background_agent_runs SET usage_json=?
  WHERE kind=? AND job_id=? AND COALESCE(usage_json,'')=''`, usageJSON, kind, jobID)
	if err != nil {
		return false, fmt.Errorf("jobstore: set background run usage: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// BackgroundDayStats is what the runs of a kind added up to since a point in time. Runs
// counts the runs that actually started (a blocked run is not one); the usage figures
// only sum runs that reported usage, so unknown usage is never counted as zero.
type BackgroundDayStats struct {
	Runs         int
	Blocked      int
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	CostUSD      float64
	// UsageRuns is how many of the runs reported usage.
	UsageRuns  int
	LastStatus string
	LastError  string
	LastJobID  string
	LastAt     int64
}

// BackgroundStatsSince sums the runs of a kind that started at or after since.
func (s *Store) BackgroundStatsSince(kind string, since int64) (BackgroundDayStats, error) {
	rows, err := s.db.Query(`SELECT status, COALESCE(usage_json,''), COALESCE(error,''), job_id, started_at
  FROM background_agent_runs WHERE kind=? AND started_at>=? ORDER BY started_at, id`, kind, since)
	if err != nil {
		return BackgroundDayStats{}, fmt.Errorf("jobstore: background stats: %w", err)
	}
	defer rows.Close()
	var st BackgroundDayStats
	for rows.Next() {
		var status, usage, errText, jobID string
		var at int64
		if err := rows.Scan(&status, &usage, &errText, &jobID, &at); err != nil {
			return BackgroundDayStats{}, fmt.Errorf("jobstore: scan background run: %w", err)
		}
		st.LastStatus, st.LastError, st.LastJobID, st.LastAt = status, errText, jobID, at
		if status == BackgroundRunBlocked {
			st.Blocked++
			continue
		}
		st.Runs++
		if usage == "" {
			continue
		}
		var u struct {
			Input  int64   `json:"input_tokens"`
			Output int64   `json:"output_tokens"`
			Total  int64   `json:"total_tokens"`
			Cost   float64 `json:"cost_usd"`
		}
		if json.Unmarshal([]byte(usage), &u) != nil {
			continue
		}
		st.UsageRuns++
		st.InputTokens += u.Input
		st.OutputTokens += u.Output
		st.TotalTokens += u.Total
		st.CostUSD += u.Cost
	}
	return st, rows.Err()
}

// migrateBackgroundAgents adds the columns the runner needs on top of the tables the
// previous release created (additive, G057: the base schema does not mention them), and
// imports the summarizer's older per-project session book once.
func (s *Store) migrateBackgroundAgents() error {
	for table, cols := range map[string][][2]string{
		"background_agent_sessions": {
			{"last_job_id", "last_job_id TEXT NOT NULL DEFAULT ''"},
			{"rotation_reason", "rotation_reason TEXT NOT NULL DEFAULT ''"},
		},
		"background_agent_runs": {
			{"project", "project TEXT NOT NULL DEFAULT ''"},
			{"mode", "mode TEXT NOT NULL DEFAULT ''"},
			{"rotation_reason", "rotation_reason TEXT NOT NULL DEFAULT ''"},
		},
	} {
		have, err := s.tableColumns(table)
		if err != nil {
			return err
		}
		for _, c := range cols {
			if _, ok := have[c[0]]; ok {
				continue
			}
			if _, err := s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + c[1]); err != nil {
				return fmt.Errorf("jobstore: migrate %s add %s: %w", table, c[0], err)
			}
		}
	}
	return s.importSummarizerSessions()
}

// importSummarizerSessions copies the rows of the summarizer's own session book
// (summarizer_sessions) into the common background session table once, as sessions of
// kind 'summarizer' bound to no agent / model yet (the runner binds them on first use).
//
// DEPRECATED(v0.208): remove in v0.211 (delete this import and the summarizer_sessions
// table together; by then every database that held a book has been opened by a newer build).
func (s *Store) importSummarizerSessions() error {
	const marker = "migrated_summarizer_sessions"
	if v, err := s.GetWorkKV(marker); err != nil || v != "" {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin summarizer session import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO background_agent_sessions
  (kind, session_id, agent, project, work_dir, model, created_at, last_used_at, runs, state, manual_reset, last_job_id)
  SELECT 'summarizer',
         CASE WHEN session_id='' THEN '` + BackgroundLocalSessionPrefix + `summarizer:'||project_key ELSE session_id END,
         '', project_key, '', '', created_at, updated_at, runs, 'active', 0, last_job_id
  FROM summarizer_sessions`); err != nil {
		return fmt.Errorf("jobstore: import summarizer sessions: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO work_kv(k, v) VALUES (?,?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, marker, "1"); err != nil {
		return fmt.Errorf("jobstore: mark summarizer session import: %w", err)
	}
	return tx.Commit()
}

// BackgroundLocalSessionPrefix marks a session id gofer made up for a run whose agent
// reported none: the row still needs a key, but the id is not an agent-side session.
const BackgroundLocalSessionPrefix = "local:"
