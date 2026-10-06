package jobstore

import (
	"fmt"
	"strings"
)

// Tracked descriptive fields of a work item (W2a field-level source, design §14.1.5):
// each of them remembers WHO last wrote it and when, so a card can say "整理器 · 3 分钟前"
// next to the goal and a reader can judge how far to trust it.
const (
	WorkFieldGoal    = "goal"
	WorkFieldBlocker = "blocker"
	WorkFieldNext    = "next"
	WorkFieldSummary = "summary"
)

// WorkFieldSource is who wrote one field and when (unix seconds).
type WorkFieldSource struct {
	By string `json:"by"`
	At int64  `json:"at"`
}

// recordFieldSources writes the source row of every tracked field that differs between
// cur and next. It runs inside UpdateWorkItem's writer lock.
func (s *Store) recordFieldSources(id string, cur, next WorkItem, by string, now int64) error {
	by = strings.TrimSpace(by)
	if by == "" {
		by = "system"
	}
	changed := make([]string, 0, 4)
	if cur.Goal != next.Goal {
		changed = append(changed, WorkFieldGoal)
	}
	if cur.BlockerText != next.BlockerText || cur.BlockerKind != next.BlockerKind {
		changed = append(changed, WorkFieldBlocker)
	}
	if cur.NextStep != next.NextStep {
		changed = append(changed, WorkFieldNext)
	}
	if cur.Summary != next.Summary {
		changed = append(changed, WorkFieldSummary)
	}
	for _, f := range changed {
		if _, err := s.db.Exec(`INSERT INTO work_field_sources(work_item_id, field, by, at) VALUES (?,?,?,?)
  ON CONFLICT(work_item_id, field) DO UPDATE SET by=excluded.by, at=excluded.at`, id, f, by, now); err != nil {
			return fmt.Errorf("jobstore: record work field source: %w", err)
		}
	}
	return nil
}

// WorkFieldSources returns the per-field sources of one item (missing = never written
// since this tracking existed).
func (s *Store) WorkFieldSources(id string) (map[string]WorkFieldSource, error) {
	rows, err := s.db.Query(`SELECT field, by, at FROM work_field_sources WHERE work_item_id = ?`, strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("jobstore: work field sources: %w", err)
	}
	defer rows.Close()
	out := map[string]WorkFieldSource{}
	for rows.Next() {
		var f string
		var fs WorkFieldSource
		if err := rows.Scan(&f, &fs.By, &fs.At); err != nil {
			return nil, err
		}
		out[f] = fs
	}
	return out, rows.Err()
}

// workFieldSourceBy reads one field's source (ok=false when it has none).
func (s *Store) workFieldSourceBy(id, field string) (WorkFieldSource, bool) {
	var fs WorkFieldSource
	err := s.db.QueryRow(`SELECT by, at FROM work_field_sources WHERE work_item_id = ? AND field = ?`, id, field).Scan(&fs.By, &fs.At)
	return fs, err == nil
}
