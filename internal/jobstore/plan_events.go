package jobstore

import (
	"fmt"
	"time"
)

// plan_events is the write-ahead log of the plan advance entry point (job.emitPlanEvent):
// a row is inserted BEFORE the advance runs and deleted once it succeeded, so a process
// that dies mid-advance leaves the row behind for the periodic scan to replay. The table
// is new and additive (G057): an older database simply gets it created on open.
var planEventSchema = []string{
	`CREATE TABLE IF NOT EXISTS plan_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, plan_id TEXT NOT NULL, todo_id TEXT NOT NULL DEFAULT '',
 job_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, caller_id TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL)`,
}

func init() { schemaStmts = append(schemaStmts, planEventSchema...) }

// PlanEventRow is one pending plan_events row. By is the authenticated caller the advance
// ran for (a supervisor-bound plan dispatches only for its owner), kept so a replay acts
// with the identity the original call had.
type PlanEventRow struct {
	ID        int64
	PlanID    string
	TodoID    string
	JobID     string
	Kind      string
	By        string
	CreatedAt int64
}

// InsertPlanEvent stores a pending event and returns its id.
func (s *Store) InsertPlanEvent(e PlanEventRow) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO plan_events(plan_id, todo_id, job_id, kind, caller_id, created_at) VALUES(?,?,?,?,?,?)`,
		e.PlanID, e.TodoID, e.JobID, e.Kind, e.By, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert plan event: %w", err)
	}
	return res.LastInsertId()
}

// DeletePlanEvent removes a processed event; a missing id is not an error.
func (s *Store) DeletePlanEvent(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM plan_events WHERE id = ?`, id); err != nil {
		return fmt.Errorf("jobstore: delete plan event: %w", err)
	}
	return nil
}

// ListPlanEvents returns the pending events oldest first, at most limit rows.
func (s *Store) ListPlanEvents(limit int) ([]PlanEventRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, plan_id, todo_id, job_id, kind, caller_id, created_at FROM plan_events ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list plan events: %w", err)
	}
	defer rows.Close()
	var out []PlanEventRow
	for rows.Next() {
		var e PlanEventRow
		if err := rows.Scan(&e.ID, &e.PlanID, &e.TodoID, &e.JobID, &e.Kind, &e.By, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan plan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
