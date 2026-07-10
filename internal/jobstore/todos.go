package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// PlanTodo is a plan-orchestration todo item. JobID "" is a plain checklist
// item; a non-empty JobID binds it to one job run as metadata. Done is exposed
// as bool while storage stays the 0/1 INTEGER column used by SQLite.
type PlanTodo struct {
	TodoID    string
	PlanID    string
	JobID     string
	Title     string
	Done      bool
	Sort      int
	CreatedAt int64
	UpdatedAt int64
}

const selectTodoCols = `SELECT todo_id, plan_id, COALESCE(job_id,''),
  COALESCE(title,''), COALESCE(done,0), COALESCE(sort,0), created_at, updated_at
  FROM plan_todos`

func scanTodo(sc rowScanner) (PlanTodo, error) {
	var (
		t    PlanTodo
		done int
	)
	err := sc.Scan(&t.TodoID, &t.PlanID, &t.JobID, &t.Title, &done, &t.Sort,
		&t.CreatedAt, &t.UpdatedAt)
	t.Done = done != 0
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
	done := 0
	if t.Done {
		done = 1
	}
	const q = `INSERT INTO plan_todos
  (todo_id, plan_id, job_id, title, done, sort, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, t.TodoID, t.PlanID, jobID, t.Title, done, t.Sort,
		t.CreatedAt, t.UpdatedAt); err != nil {
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

// SetTodoDone sets a todo's done flag. P3 keeps this purely manual; job terminal
// state does not drive this field.
func (s *Store) SetTodoDone(todoID string, done bool) (bool, error) {
	d := 0
	if done {
		d = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_todos SET done=?, updated_at=? WHERE todo_id=?`,
		d, s.unixNow(), todoID)
	if err != nil {
		return false, fmt.Errorf("jobstore: set todo %q done: %w", todoID, err)
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
