package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Plan status values. A plan is a lightweight grouping header; it does not
// advance jobs itself.
const (
	PlanIDMinLength = 9
	PlanOpen        = "open"
	PlanActive      = "active"
	PlanDone        = "done"
	PlanArchived    = "archived"
)

// Plan is the SQLite-persisted plan grouping header. It is neutral (no
// internal/job import) so job/http layers can drive it without an import cycle.
type Plan struct {
	PlanID      string
	Title       string
	Description string
	Status      string
	Owner       string
	Progress    int
	CreatedAt   int64
	UpdatedAt   int64
}

const selectPlanCols = `SELECT plan_id, COALESCE(title,''), COALESCE(description,''),
  status, COALESCE(owner,''), COALESCE(progress,0), created_at, updated_at FROM plans`

func scanPlan(sc rowScanner) (Plan, error) {
	var p Plan
	err := sc.Scan(&p.PlanID, &p.Title, &p.Description, &p.Status, &p.Owner,
		&p.Progress, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// InsertPlan persists a new plan header. The caller must generate a non-empty id;
// jobstore stays job-import-free.
func (s *Store) InsertPlan(p Plan) error {
	if p.PlanID == "" {
		return errors.New("jobstore: InsertPlan: empty plan id")
	}
	if p.Status == "" {
		p.Status = PlanOpen
	}
	const q = `INSERT INTO plans
  (plan_id, title, description, status, owner, progress, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, p.PlanID, p.Title, p.Description, p.Status, p.Owner,
		p.Progress, p.CreatedAt, p.UpdatedAt); err != nil {
		return fmt.Errorf("jobstore: insert plan %q: %w", p.PlanID, err)
	}
	return nil
}

// GetPlan returns the plan by id. ok is false with nil error when absent.
func (s *Store) GetPlan(id string) (Plan, bool, error) {
	p, err := scanPlan(s.db.QueryRow(selectPlanCols+" WHERE plan_id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, false, nil
	}
	if err != nil {
		return Plan{}, false, fmt.Errorf("jobstore: get plan %q: %w", id, err)
	}
	return p, true, nil
}

// ListPlans returns plans, optionally filtered by status, newest first.
func (s *Store) ListPlans(status string, limit int) ([]Plan, error) {
	query := selectPlanCols
	var args []any
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC, rowid DESC"
	if limit <= 0 {
		limit = DefaultListLimit
	}
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list plans: %w", err)
	}
	defer rows.Close()
	out := make([]Plan, 0)
	for rows.Next() {
		p, scanErr := scanPlan(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan plan row: %w", scanErr)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list plans rows: %w", err)
	}
	return out, nil
}

// SetPlanStatus moves a plan to status and optionally updates progress.
// progress < 0 keeps the current progress value.
func (s *Store) SetPlanStatus(id, status string, progress int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var err error
	if progress < 0 {
		_, err = s.db.Exec(`UPDATE plans SET status=?, updated_at=? WHERE plan_id=?`,
			status, s.unixNow(), id)
	} else {
		_, err = s.db.Exec(`UPDATE plans SET status=?, progress=?, updated_at=? WHERE plan_id=?`,
			status, progress, s.unixNow(), id)
	}
	if err != nil {
		return fmt.Errorf("jobstore: set plan %q status %s: %w", id, status, err)
	}
	return nil
}

// AttachJobToPlan binds an existing job to a plan by setting jobs.plan_id. It
// returns false with nil error when the job id is unknown.
func (s *Store) AttachJobToPlan(jobID, planID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE jobs SET plan_id = ? WHERE id = ?`, planID, jobID)
	if err != nil {
		return false, fmt.Errorf("jobstore: attach job %q to plan %q: %w", jobID, planID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// TouchPlan bumps a plan's updated_at after membership/progress changes.
func (s *Store) TouchPlan(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE plans SET updated_at=? WHERE plan_id=?`, s.unixNow(), id); err != nil {
		return fmt.Errorf("jobstore: touch plan %q: %w", id, err)
	}
	return nil
}

// PlanCounts is the query-time live roll-up of a plan's jobs by status bucket.
type PlanCounts struct {
	Total   int `json:"total"`
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
}

// PlanTodoCounts is the query-time roll-up of a plan's todos by lifecycle status.
type PlanTodoCounts struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Doing   int `json:"doing"`
	Done    int `json:"done"`
	Skipped int `json:"skipped"`
}

// add counts n todos of status st (an unknown/legacy status counts as pending).
func (c *PlanTodoCounts) add(st string, n int) {
	c.Total += n
	switch st {
	case TodoDoing:
		c.Doing += n
	case TodoDone:
		c.Done += n
	case TodoSkipped:
		c.Skipped += n
	default:
		c.Pending += n
	}
}

// CountTodos rolls up already-loaded todos (the plan detail has them in hand).
func CountTodos(todos []PlanTodo) PlanTodoCounts {
	var c PlanTodoCounts
	for _, t := range todos {
		c.add(t.Status, 1)
	}
	return c
}

// PlanCompletion.Basis values.
const (
	CompletionTodos = "todos"
	CompletionJobs  = "jobs"
	CompletionNone  = "none"
)

// PlanCompletion is a plan's single progress figure. Todos are the planned work
// items, so they win whenever a plan has any (done + skipped count as complete).
// Only a todo-less plan falls back to its jobs (succeeded / all): jobs include
// retries and failed attempts, which makes them a poor primary measure. Percent is
// nil when there is nothing to measure, so clients show "—" rather than 0%.
type PlanCompletion struct {
	Basis   string `json:"basis"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Percent *int   `json:"percent"`
}

// RollupPlanCompletion applies the PlanCompletion rule to a plan's job and todo
// roll-ups. It is the only place the rule lives; API, CLI and web consume it.
func RollupPlanCompletion(j PlanCounts, t PlanTodoCounts) PlanCompletion {
	c := PlanCompletion{Basis: CompletionNone}
	switch {
	case t.Total > 0:
		c.Basis, c.Done, c.Total = CompletionTodos, t.Done+t.Skipped, t.Total
	case j.Total > 0:
		c.Basis, c.Done, c.Total = CompletionJobs, j.Done, j.Total
	}
	if c.Total > 0 {
		p := (c.Done*100 + c.Total/2) / c.Total
		c.Percent = &p
	}
	return c
}

// todoCountsChunk bounds the IN (...) list per query, well under SQLite's
// host-parameter limit.
const todoCountsChunk = 500

// PlanTodoCountsByPlan rolls up todo statuses for many plans with one grouped
// query per chunk (the plan list must not issue a query per plan). Plans without
// todos are absent from the map, i.e. read as the zero PlanTodoCounts.
func (s *Store) PlanTodoCountsByPlan(planIDs []string) (map[string]PlanTodoCounts, error) {
	out := make(map[string]PlanTodoCounts, len(planIDs))
	for start := 0; start < len(planIDs); start += todoCountsChunk {
		chunk := planIDs[start:min(start+todoCountsChunk, len(planIDs))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		query := `SELECT plan_id, status, COUNT(*) FROM plan_todos WHERE plan_id IN (` +
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + `) GROUP BY plan_id, status`
		if err := s.scanTodoCounts(query, args, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanTodoCounts runs one grouped (plan_id, status, count) query into out.
func (s *Store) scanTodoCounts(query string, args []any, out map[string]PlanTodoCounts) error {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("jobstore: plan todo counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			planID, status string
			n              int
		)
		if err := rows.Scan(&planID, &status, &n); err != nil {
			return fmt.Errorf("jobstore: scan plan todo counts: %w", err)
		}
		c := out[planID]
		c.add(status, n)
		out[planID] = c
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("jobstore: plan todo count rows: %w", err)
	}
	return nil
}

// PlanJobStatusCounts returns a raw status->count map for jobs bound to planID.
// It uses a full GROUP BY query so counts are not affected by any job list limit.
func (s *Store) PlanJobStatusCounts(planID string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs WHERE plan_id = ? GROUP BY status`, planID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: plan job status counts %q: %w", planID, err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("jobstore: scan plan job status %q: %w", planID, err)
		}
		out[status] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: plan job status rows %q: %w", planID, err)
	}
	return out, nil
}

// RollupPlanCounts folds raw job statuses into the public five-bucket summary.
// Status strings stay hard-coded here so jobstore remains neutral and does not
// import internal/job.
func RollupPlanCounts(raw map[string]int) PlanCounts {
	var c PlanCounts
	for st, n := range raw {
		c.Total += n
		switch st {
		case "queued":
			c.Queued += n
		case "running", "pending_interaction":
			c.Running += n
		case "done":
			c.Done += n
		case "failed", "timeout", "cancelled":
			c.Failed += n
		}
	}
	return c
}
