package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Memory candidates (gofer-3nxa.2, design 2026-10-10-handoff-brief-and-knowledge-loop
// §三): the items of the 「## 可复用经验」 section of a finished job's report. Nothing is
// written to memory automatically — a person accepts a candidate (it becomes a scoped
// memory, see job.Service.AcceptMemoryCandidate) or rejects it. This file only stores
// the rows.

// Memory candidate states.
const (
	MemoryCandidatePending  = "pending"
	MemoryCandidateAccepted = "accepted"
	MemoryCandidateRejected = "rejected"
)

// ErrMemoryCandidateNotFound is an unknown candidate id.
var ErrMemoryCandidateNotFound = errors.New("memory candidate not found")

// ErrMemoryCandidateDecided is a decision on a candidate that is no longer pending.
var ErrMemoryCandidateDecided = errors.New("memory candidate already decided")

// UNIQUE(job_id, text) is what makes the capture idempotent: a job that reaches the
// terminal path twice (adoption after a restart, a review after needs_review) records
// each item once.
var memoryCandidateSchema = []string{
	`CREATE TABLE IF NOT EXISTS memory_candidates (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id      TEXT NOT NULL,
  project_key TEXT NOT NULL DEFAULT '',
  text        TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending',
  memory_key  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  decided_at  INTEGER NOT NULL DEFAULT 0,
  decided_by  TEXT NOT NULL DEFAULT '',
  UNIQUE(job_id, text)
)`,
	`CREATE INDEX IF NOT EXISTS idx_memory_candidates_status ON memory_candidates(status, project_key, created_at)`,
}

func init() { schemaStmts = append(schemaStmts, memoryCandidateSchema...) }

// MemoryCandidate is one memory_candidates row. MemoryKey is the key the accepted
// memory was written under ("" until accepted).
type MemoryCandidate struct {
	ID         int64  `json:"id"`
	JobID      string `json:"job_id"`
	ProjectKey string `json:"project_key"`
	Text       string `json:"text"`
	Status     string `json:"status"`
	MemoryKey  string `json:"memory_key,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	DecidedAt  int64  `json:"decided_at,omitempty"`
	DecidedBy  string `json:"decided_by,omitempty"`
}

// MemoryCandidateFilter narrows ListMemoryCandidates. Status "" = pending; "all" =
// every state.
type MemoryCandidateFilter struct {
	JobID      string
	ProjectKey string
	Status     string
}

const memoryCandidateCols = `id,job_id,project_key,text,status,memory_key,created_at,decided_at,decided_by`

func scanMemoryCandidate(sc interface{ Scan(...any) error }) (MemoryCandidate, error) {
	var m MemoryCandidate
	err := sc.Scan(&m.ID, &m.JobID, &m.ProjectKey, &m.Text, &m.Status, &m.MemoryKey, &m.CreatedAt, &m.DecidedAt, &m.DecidedBy)
	return m, err
}

// AddMemoryCandidates records texts as pending candidates of jobID, skipping empty
// texts and ones this job already has. It returns how many rows were new.
func (s *Store) AddMemoryCandidates(jobID, projectKey string, texts []string) (int, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return 0, fmt.Errorf("%w: job_id is required", ErrWorkInvalid)
	}
	now := s.unixNow()
	added := 0
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, text := range texts {
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		res, err := s.db.Exec(`INSERT OR IGNORE INTO memory_candidates (job_id,project_key,text,status,created_at) VALUES (?,?,?,?,?)`,
			jobID, projectKey, text, MemoryCandidatePending, now)
		if err != nil {
			return added, fmt.Errorf("jobstore: add memory candidate: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	return added, nil
}

// GetMemoryCandidate reads one candidate.
func (s *Store) GetMemoryCandidate(id int64) (MemoryCandidate, error) {
	m, err := scanMemoryCandidate(s.db.QueryRow(`SELECT `+memoryCandidateCols+` FROM memory_candidates WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryCandidate{}, ErrMemoryCandidateNotFound
	}
	if err != nil {
		return MemoryCandidate{}, fmt.Errorf("jobstore: get memory candidate: %w", err)
	}
	return m, nil
}

// ListMemoryCandidates lists candidates, oldest first.
func (s *Store) ListMemoryCandidates(f MemoryCandidateFilter) ([]MemoryCandidate, error) {
	var (
		where []string
		args  []any
	)
	switch status := strings.TrimSpace(f.Status); status {
	case "all":
	case "":
		where, args = append(where, "status=?"), append(args, MemoryCandidatePending)
	default:
		where, args = append(where, "status=?"), append(args, status)
	}
	if f.JobID != "" {
		where, args = append(where, "job_id=?"), append(args, f.JobID)
	}
	if f.ProjectKey != "" {
		where, args = append(where, "project_key=?"), append(args, f.ProjectKey)
	}
	q := `SELECT ` + memoryCandidateCols + ` FROM memory_candidates`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	rows, err := s.db.Query(q+` ORDER BY created_at ASC, id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list memory candidates: %w", err)
	}
	defer rows.Close()
	out := make([]MemoryCandidate, 0)
	for rows.Next() {
		m, err := scanMemoryCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan memory candidate: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DecideMemoryCandidate moves a candidate from one state to another, recording the
// memory key (accepted) and who decided. It is a compare-and-set on the current
// state: a candidate not in `from` returns ErrMemoryCandidateDecided, so two
// concurrent decisions cannot both win. from = accepted / to = pending is the undo
// of a claim whose memory write failed.
func (s *Store) DecideMemoryCandidate(id int64, from, to, memoryKey, by string) (MemoryCandidate, error) {
	switch to {
	case MemoryCandidatePending, MemoryCandidateAccepted, MemoryCandidateRejected:
	default:
		return MemoryCandidate{}, fmt.Errorf("%w: unknown memory candidate status %q", ErrWorkInvalid, to)
	}
	decidedAt := s.unixNow()
	if to == MemoryCandidatePending {
		decidedAt, memoryKey, by = 0, "", ""
	}
	s.writeMu.Lock()
	res, err := s.db.Exec(`UPDATE memory_candidates SET status=?, memory_key=?, decided_at=?, decided_by=? WHERE id=? AND status=?`,
		to, memoryKey, decidedAt, by, id, from)
	s.writeMu.Unlock()
	if err != nil {
		return MemoryCandidate{}, fmt.Errorf("jobstore: decide memory candidate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := s.GetMemoryCandidate(id); gerr != nil {
			return MemoryCandidate{}, gerr
		}
		return MemoryCandidate{}, ErrMemoryCandidateDecided
	}
	return s.GetMemoryCandidate(id)
}
