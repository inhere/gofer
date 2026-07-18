package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// Todo lifecycle statuses (Part C §C2). Done bool is kept in lockstep for
// back-compat: Done ⟺ Status == TodoDone (skipped is terminal but NOT done).
const (
	TodoPending = "pending"
	TodoDoing   = "doing"
	TodoDone    = "done"
	TodoSkipped = "skipped"
)

// ValidTodoStatus reports whether s is one of the todo lifecycle statuses.
func ValidTodoStatus(s string) bool {
	switch s {
	case TodoPending, TodoDoing, TodoDone, TodoSkipped:
		return true
	}
	return false
}

// PlanTodo is a plan-orchestration todo item. JobID "" is a plain checklist
// item; a non-empty JobID binds it to one job run as metadata. Done is exposed
// as bool while storage stays the 0/1 INTEGER column used by SQLite; Status is
// the richer lifecycle (Part C §C2), StartedAt/DoneAt are stamped automatically
// on status transitions, Note is a short outcome/remark (overwritten, not a
// process log — that belongs to job logs).
type PlanTodo struct {
	TodoID    string
	PlanID    string
	JobID     string
	Title     string
	Done      bool
	Status    string
	StartedAt int64
	DoneAt    int64
	Note      string
	Sort      int
	CreatedAt int64
	UpdatedAt int64
}

const selectTodoCols = `SELECT todo_id, plan_id, COALESCE(job_id,''),
  COALESCE(title,''), COALESCE(done,0), COALESCE(status,''),
  COALESCE(started_at,0), COALESCE(done_at,0), COALESCE(note,''),
  COALESCE(sort,0), created_at, updated_at
  FROM plan_todos`

func scanTodo(sc rowScanner) (PlanTodo, error) {
	var (
		t    PlanTodo
		done int
	)
	err := sc.Scan(&t.TodoID, &t.PlanID, &t.JobID, &t.Title, &done, &t.Status,
		&t.StartedAt, &t.DoneAt, &t.Note, &t.Sort, &t.CreatedAt, &t.UpdatedAt)
	t.Done = done != 0
	if t.Status == "" {
		// Rows written before the lifecycle columns (or by an old binary racing the
		// migration backfill) surface a status derived from done.
		if t.Done {
			t.Status = TodoDone
		} else {
			t.Status = TodoPending
		}
	}
	return t, err
}

// InsertTodo persists a new todo. The caller must generate a non-empty todo id.
// A blank JobID is stored as NULL and scans back as "" via COALESCE.
func (s *Store) InsertTodo(t PlanTodo) error {
	if t.TodoID == "" {
		return errors.New("jobstore: InsertTodo: empty todo id")
	}
	if t.PlanID == "" {
		return errors.New("jobstore: InsertTodo: empty plan id")
	}
	var jobID any
	if t.JobID != "" {
		jobID = t.JobID
	}
	status := t.Status
	if status == "" {
		if t.Done {
			status = TodoDone
		} else {
			status = TodoPending
		}
	}
	if !ValidTodoStatus(status) {
		return fmt.Errorf("jobstore: InsertTodo: invalid status %q", status)
	}
	done := 0
	if status == TodoDone {
		done = 1
	}
	const q = `INSERT INTO plan_todos
  (todo_id, plan_id, job_id, title, done, status, started_at, done_at, note, sort, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, t.TodoID, t.PlanID, jobID, t.Title, done, status,
		t.StartedAt, t.DoneAt, t.Note, t.Sort, t.CreatedAt, t.UpdatedAt); err != nil {
		return fmt.Errorf("jobstore: insert todo %q: %w", t.TodoID, err)
	}
	return nil
}

// GetTodo returns a todo by id. ok is false with nil error when absent.
func (s *Store) GetTodo(id string) (PlanTodo, bool, error) {
	t, err := scanTodo(s.db.QueryRow(selectTodoCols+" WHERE todo_id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return PlanTodo{}, false, nil
	}
	if err != nil {
		return PlanTodo{}, false, fmt.Errorf("jobstore: get todo %q: %w", id, err)
	}
	return t, true, nil
}

// ListTodosByPlan returns a plan's todos in stable display order.
func (s *Store) ListTodosByPlan(planID string) ([]PlanTodo, error) {
	rows, err := s.db.Query(
		selectTodoCols+" WHERE plan_id = ? ORDER BY sort ASC, created_at ASC, todo_id ASC",
		planID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list todos of plan %q: %w", planID, err)
	}
	defer rows.Close()
	out := make([]PlanTodo, 0)
	for rows.Next() {
		t, scanErr := scanTodo(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan todo row: %w", scanErr)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list todos of plan %q rows: %w", planID, err)
	}
	return out, nil
}

// SetTodoDone sets a todo's done flag (legacy二态 surface). It maps onto the
// lifecycle: done=true → TodoDone, done=false → TodoPending, with the same
// automatic timestamps as UpdateTodoStatus.
func (s *Store) SetTodoDone(todoID string, done bool) (bool, error) {
	status := TodoPending
	if done {
		status = TodoDone
	}
	return s.UpdateTodoStatus(todoID, status, nil)
}

// UpdateTodoStatus moves a todo along its lifecycle and/or updates its note
// (Part C §C2). status "" keeps the current status (note-only update); note nil
// keeps the current note (status-only update). Timestamps are stamped on the
// transition itself:
//
//   - → doing: started_at is set (only if still 0 — a redo keeps the original
//     start), done_at is cleared (done → doing = redo);
//   - → done/skipped: done_at is set;
//   - → pending: both cleared (full reset).
//
// The legacy done flag stays in lockstep (done ⟺ status=done).
func (s *Store) UpdateTodoStatus(todoID, status string, note *string) (bool, error) {
	if status != "" && !ValidTodoStatus(status) {
		return false, fmt.Errorf("jobstore: update todo %q: invalid status %q", todoID, status)
	}
	now := s.unixNow()
	var noteVal any // nil = keep current note (COALESCE)
	if note != nil {
		noteVal = *note
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// Single conditional UPDATE: all timestamp rules live in SQL against the
	// CURRENT row values, so a concurrent updater can never interleave a read-
	// modify-write (same reasoning as the SR303-style conditional updates).
	const q = `UPDATE plan_todos SET
  status     = CASE WHEN ?1 = '' THEN COALESCE(NULLIF(status,''), CASE WHEN done=1 THEN 'done' ELSE 'pending' END) ELSE ?1 END,
  done       = CASE WHEN ?1 = '' THEN done WHEN ?1 = 'done' THEN 1 ELSE 0 END,
  started_at = CASE WHEN ?1 = 'doing' AND COALESCE(started_at,0) = 0 THEN ?2
                    WHEN ?1 = 'pending' THEN 0
                    ELSE COALESCE(started_at,0) END,
  done_at    = CASE WHEN ?1 IN ('done','skipped') THEN ?2
                    WHEN ?1 IN ('doing','pending') THEN 0
                    ELSE COALESCE(done_at,0) END,
  note       = COALESCE(?3, note),
  updated_at = ?2
  WHERE todo_id = ?4`
	res, err := s.db.Exec(q, status, now, noteVal, todoID)
	if err != nil {
		return false, fmt.Errorf("jobstore: update todo %q status: %w", todoID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteTodo removes a todo. P3 keeps this as store-only CRUD; no HTTP/MCP/CLI
// delete surface is exposed.
func (s *Store) DeleteTodo(todoID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM plan_todos WHERE todo_id=?`, todoID)
	if err != nil {
		return false, fmt.Errorf("jobstore: delete todo %q: %w", todoID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
