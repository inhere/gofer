package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Memory hygiene suggestions (design docs/design/2026-10-09-prime-memory-quality-design.md
// §2.6, P4): the steward proposes ONE change to one repo-tracker memory of the server
// mirror (archive / merge into another key / change kind / add a summary / add when
// keywords); a person adopts or dismisses it on the 「今天」 page. internal/today owns the
// validation and the apply step; this file only stores the rows.

// Memory suggestion states.
const (
	MemorySuggestPending   = "pending"
	MemorySuggestAdopted   = "adopted"
	MemorySuggestDismissed = "dismissed"
	// MemorySuggestStale: the memory changed under the suggestion (deleted, or the merge
	// target is gone) before anyone decided; it leaves the queue.
	MemorySuggestStale = "stale"
)

// ErrMemorySuggestionNotFound is an unknown suggestion id.
var ErrMemorySuggestionNotFound = errors.New("memory suggestion not found")

// ErrMemorySuggestionDecided is a decision on a suggestion that is no longer pending.
var ErrMemorySuggestionDecided = errors.New("memory suggestion already decided")

var memorySuggestionSchema = []string{
	`CREATE TABLE IF NOT EXISTS memory_suggestions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  tracker_id   TEXT NOT NULL,
  memory_key   TEXT NOT NULL,
  action       TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  reason       TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL DEFAULT 'pending',
  by           TEXT NOT NULL DEFAULT '',
  job_id       TEXT NOT NULL DEFAULT '',
  base_rev     INTEGER NOT NULL DEFAULT 0,
  target_rev   INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  decided_at   INTEGER NOT NULL DEFAULT 0,
  decided_by   TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX IF NOT EXISTS idx_memory_suggestions_key ON memory_suggestions(tracker_id, memory_key, action, state)`,
	`CREATE INDEX IF NOT EXISTS idx_memory_suggestions_state ON memory_suggestions(state, created_at)`,
}

func init() { schemaStmts = append(schemaStmts, memorySuggestionSchema...) }

// MemorySuggestion is one memory_suggestions row. PayloadJSON is the action's proposed
// value (internal/today.MemoryPayload).
type MemorySuggestion struct {
	ID          int64  `json:"id"`
	TrackerID   string `json:"tracker_id"`
	MemoryKey   string `json:"memory_key"`
	Action      string `json:"action"`
	PayloadJSON string `json:"payload_json"`
	Reason      string `json:"reason"`
	State       string `json:"state"`
	By          string `json:"by,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	// BaseRev is the memory's rev when the suggestion was made; TargetRev the merge
	// target's (0 for other actions and rows from before the column). Adoption only
	// applies while the live revs still match.
	BaseRev   int64  `json:"base_rev"`
	TargetRev int64  `json:"target_rev,omitempty"`
	CreatedAt int64  `json:"created_at"`
	DecidedAt int64  `json:"decided_at,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	Note      string `json:"note,omitempty"`
}

const memorySuggestionCols = `id,tracker_id,memory_key,action,payload_json,reason,state,by,job_id,base_rev,target_rev,created_at,decided_at,decided_by,note`

func scanMemorySuggestion(sc interface{ Scan(...any) error }) (MemorySuggestion, error) {
	var m MemorySuggestion
	err := sc.Scan(&m.ID, &m.TrackerID, &m.MemoryKey, &m.Action, &m.PayloadJSON, &m.Reason, &m.State, &m.By, &m.JobID,
		&m.BaseRev, &m.TargetRev, &m.CreatedAt, &m.DecidedAt, &m.DecidedBy, &m.Note)
	return m, err
}

// AddMemorySuggestion records a pending suggestion. The same (tracker, key, action) still
// pending is returned as is with recorded=false (no duplicate card).
func (s *Store) AddMemorySuggestion(m MemorySuggestion) (MemorySuggestion, bool, error) {
	m.TrackerID, m.MemoryKey, m.Action = strings.TrimSpace(m.TrackerID), strings.TrimSpace(m.MemoryKey), strings.TrimSpace(m.Action)
	if m.TrackerID == "" || m.MemoryKey == "" || m.Action == "" {
		return MemorySuggestion{}, false, fmt.Errorf("%w: tracker_id, key and action are required", ErrWorkInvalid)
	}
	if m.PayloadJSON == "" {
		m.PayloadJSON = "{}"
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = s.unixNow()
	}
	m.State = MemorySuggestPending
	defer s.emit(Change{Kind: ChangeWork}) // the 「今天」 page refreshes on the work topic
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ex, err := scanMemorySuggestion(s.db.QueryRow(`SELECT `+memorySuggestionCols+` FROM memory_suggestions
  WHERE tracker_id=? AND memory_key=? AND action=? AND state=? ORDER BY id DESC LIMIT 1`, m.TrackerID, m.MemoryKey, m.Action, MemorySuggestPending))
	if err == nil {
		return ex, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MemorySuggestion{}, false, fmt.Errorf("jobstore: find memory suggestion: %w", err)
	}
	res, err := s.db.Exec(`INSERT INTO memory_suggestions (tracker_id,memory_key,action,payload_json,reason,state,by,job_id,base_rev,target_rev,created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`, m.TrackerID, m.MemoryKey, m.Action, m.PayloadJSON, m.Reason, m.State, m.By, m.JobID, m.BaseRev, m.TargetRev, m.CreatedAt)
	if err != nil {
		return MemorySuggestion{}, false, fmt.Errorf("jobstore: add memory suggestion: %w", err)
	}
	m.ID, _ = res.LastInsertId()
	return m, true, nil
}

// GetMemorySuggestion reads one suggestion.
func (s *Store) GetMemorySuggestion(id int64) (MemorySuggestion, error) {
	m, err := scanMemorySuggestion(s.db.QueryRow(`SELECT `+memorySuggestionCols+` FROM memory_suggestions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MemorySuggestion{}, ErrMemorySuggestionNotFound
	}
	if err != nil {
		return MemorySuggestion{}, fmt.Errorf("jobstore: get memory suggestion: %w", err)
	}
	return m, nil
}

// ListMemorySuggestions lists the suggestions in one state ("" = every state), oldest first.
func (s *Store) ListMemorySuggestions(state string) ([]MemorySuggestion, error) {
	q := `SELECT ` + memorySuggestionCols + ` FROM memory_suggestions`
	var args []any
	if state != "" {
		q += ` WHERE state=?`
		args = append(args, state)
	}
	rows, err := s.db.Query(q+` ORDER BY created_at ASC, id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list memory suggestions: %w", err)
	}
	defer rows.Close()
	out := make([]MemorySuggestion, 0)
	for rows.Next() {
		m, err := scanMemorySuggestion(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan memory suggestion: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DecideMemorySuggestion moves a pending suggestion to state (adopted / dismissed / stale).
// A suggestion that is no longer pending returns ErrMemorySuggestionDecided.
func (s *Store) DecideMemorySuggestion(id int64, state, by, note string) (MemorySuggestion, error) {
	switch state {
	case MemorySuggestAdopted, MemorySuggestDismissed, MemorySuggestStale:
	default:
		return MemorySuggestion{}, fmt.Errorf("%w: unknown memory suggestion state %q", ErrWorkInvalid, state)
	}
	defer s.emit(Change{Kind: ChangeWork})
	s.writeMu.Lock()
	res, err := s.db.Exec(`UPDATE memory_suggestions SET state=?, decided_at=?, decided_by=?, note=? WHERE id=? AND state=?`,
		state, s.unixNow(), by, note, id, MemorySuggestPending)
	s.writeMu.Unlock()
	if err != nil {
		return MemorySuggestion{}, fmt.Errorf("jobstore: decide memory suggestion: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := s.GetMemorySuggestion(id); gerr != nil {
			return MemorySuggestion{}, gerr
		}
		return MemorySuggestion{}, ErrMemorySuggestionDecided
	}
	return s.GetMemorySuggestion(id)
}

// MemorySuggestionDismissedSince reports whether (tracker, key, action) was dismissed at or
// after since (the steward's re-propose cooldown).
func (s *Store) MemorySuggestionDismissedSince(trackerID, key, action string, since int64) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memory_suggestions WHERE tracker_id=? AND memory_key=? AND action=? AND state=? AND decided_at>=?`,
		trackerID, key, action, MemorySuggestDismissed, since).Scan(&n); err != nil {
		return false, fmt.Errorf("jobstore: memory suggestion cooldown: %w", err)
	}
	return n > 0, nil
}

// CountMemorySuggestionsSince counts the suggestions created at or after since (the daily cap).
func (s *Store) CountMemorySuggestionsSince(since int64) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memory_suggestions WHERE created_at>=?`, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: count memory suggestions: %w", err)
	}
	return n, nil
}

// GetTrackerMemory reads one mirrored memory (tombstones included); ok is false when the
// key was never mirrored.
func (s *Store) GetTrackerMemory(trackerID, key string) (TrackerRecord, bool, error) {
	var rec TrackerRecord
	var body string
	var deleted int
	err := s.db.QueryRow(`SELECT memory_key,body_json,rev,updated_at,deleted,deleted_at,deleted_by,changed_seq FROM tracker_memories WHERE tracker_id=? AND memory_key=?`,
		trackerID, key).Scan(&rec.ID, &body, &rec.Rev, &rec.UpdatedAt, &deleted, &rec.DeletedAt, &rec.DeletedBy, &rec.ChangedSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackerRecord{}, false, nil
	}
	if err != nil {
		return TrackerRecord{}, false, fmt.Errorf("jobstore: get tracker memory: %w", err)
	}
	rec.TrackerID, rec.Body, rec.Deleted = trackerID, []byte(body), deleted != 0
	return rec, true, nil
}
