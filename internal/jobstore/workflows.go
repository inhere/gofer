package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// unixNow stamps updated_at on the workflow mutation paths (AdvanceCurrentStep /
// SetWorkflowStatus). updated_at is bookkeeping only — it does not gate推进幂等
// (that is the conditional UPDATE's RowsAffected) — so a direct clock read is
// fine; the job package owns the authoritative job timestamps.
func (s *Store) unixNow() int64 { return time.Now().Unix() }

// Workflow status values (工作流/job 链, design §5.1). A workflow starts running,
// then reaches one terminal state: done (every step done), failed (fail-fast: a
// step failed/timed out/was cancelled, or starting the next step errored) or
// cancelled (CancelWorkflow). Only a running workflow is advanced by the engine.
const (
	WorkflowRunning   = "running"
	WorkflowDone      = "done"
	WorkflowFailed    = "failed"
	WorkflowCancelled = "cancelled"
)

// Workflow is the SQLite-persisted projection of one job-chain header (design
// §5.1). Like JobRecord it is a neutral struct (no internal/job import) so the
// job package can drive it without forming an import cycle.
//
// CurrentStep is the 1-based active step (the step whose job is currently running
// or just finished). TotalSteps is len(spec.Steps). SpecJSON is the marshalled
// WorkflowSpec the engine rebuilds each step's JobRequest from. CallerID is
// inherited by every step-job (D8). Error holds the fail reason when Status=failed.
type Workflow struct {
	ID          string
	Title       string
	Status      string
	CurrentStep int
	TotalSteps  int
	SpecJSON    string
	CallerID    string
	Error       string
	CreatedAt   int64
	UpdatedAt   int64
}

// selectWorkflowCols is the shared projection. COALESCE guards the nullable
// title/caller_id/error so a NULL scans into "" instead of failing the scan.
const selectWorkflowCols = `SELECT id, COALESCE(title,''), status, current_step,
  total_steps, spec_json, COALESCE(caller_id,''), COALESCE(error,''),
  created_at, updated_at FROM workflows`

// scanWorkflow reads one row (in selectWorkflowCols order) into a Workflow.
func scanWorkflow(sc rowScanner) (Workflow, error) {
	var w Workflow
	err := sc.Scan(
		&w.ID, &w.Title, &w.Status, &w.CurrentStep,
		&w.TotalSteps, &w.SpecJSON, &w.CallerID, &w.Error,
		&w.CreatedAt, &w.UpdatedAt,
	)
	return w, err
}

// InsertWorkflow persists a new workflow header row. created_at/updated_at are
// taken from w as given (caller passes the current time so the store stays
// clock-free / testable). Writes go through s.writeMu like every other writer so
// SQLite never sees two concurrent writers.
func (s *Store) InsertWorkflow(w Workflow) error {
	if w.ID == "" {
		return errors.New("jobstore: InsertWorkflow: empty workflow id")
	}
	if w.Status == "" {
		w.Status = WorkflowRunning
	}
	const q = `INSERT INTO workflows
  (id, title, status, current_step, total_steps, spec_json, caller_id, error, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q,
		w.ID, w.Title, w.Status, w.CurrentStep, w.TotalSteps,
		w.SpecJSON, w.CallerID, w.Error, w.CreatedAt, w.UpdatedAt,
	); err != nil {
		return fmt.Errorf("jobstore: insert workflow %q: %w", w.ID, err)
	}
	return nil
}

// GetWorkflow returns the workflow by id. The bool is false (nil error) when no
// such workflow exists, distinguishing "not found" from a real query error.
func (s *Store) GetWorkflow(id string) (Workflow, bool, error) {
	w, err := scanWorkflow(s.db.QueryRow(selectWorkflowCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, false, nil
	}
	if err != nil {
		return Workflow{}, false, fmt.Errorf("jobstore: get workflow %q: %w", id, err)
	}
	return w, true, nil
}

// ListWorkflows returns workflows, optionally filtered by status (exact match
// when non-empty), newest first (created_at desc, id desc as a stable
// tiebreaker), capped at limit (<= 0 => DefaultListLimit). It feeds both the
// list API and the sweeper's running-workflow scan (ListWorkflows("running", N)).
func (s *Store) ListWorkflows(status string, limit int) ([]Workflow, error) {
	query := selectWorkflowCols
	var args []any
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC, id DESC"
	if limit <= 0 {
		limit = DefaultListLimit
	}
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workflows: %w", err)
	}
	defer rows.Close()
	out := make([]Workflow, 0)
	for rows.Next() {
		w, scanErr := scanWorkflow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan workflow row: %w", scanErr)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workflows rows: %w", err)
	}
	return out, nil
}

// AdvanceCurrentStep is the推进幂等屏障 (SR303). It atomically moves a running
// workflow's current_step from `from` to `to` via a conditional UPDATE, and
// returns true only for the UPDATE that actually changed the row.
//
// The WHERE re-checks BOTH `current_step = from` AND `status = 'running'`, so two
// concurrent or repeated callers (the finish hook and the sweeper firing for the
// SAME just-finished step) can never both succeed: the first moves current_step
// off `from`, the second affects 0 rows and returns false. The single true is the
// only one that proceeds to start the next step — a step is therefore never
// started twice. Runs under writeMu (every writer's in-process lock).
func (s *Store) AdvanceCurrentStep(id string, from, to int) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE workflows SET current_step = ?, updated_at = ?
     WHERE id = ? AND current_step = ? AND status = ?`,
		to, s.unixNow(), id, from, WorkflowRunning,
	)
	if err != nil {
		return false, fmt.Errorf("jobstore: advance workflow %q %d->%d: %w", id, from, to, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetWorkflowStatus moves a workflow to a terminal/explicit status, recording an
// optional error message and stamping updated_at. Idempotent re-writes are
// harmless (same terminal status). Runs under writeMu.
func (s *Store) SetWorkflowStatus(id, status, errMsg string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var em any
	if errMsg != "" {
		em = errMsg
	}
	if _, err := s.db.Exec(
		`UPDATE workflows SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, em, s.unixNow(), id,
	); err != nil {
		return fmt.Errorf("jobstore: set workflow %q status %s: %w", id, status, err)
	}
	return nil
}

// ListWorkflowJobs returns every step-job of a workflow, ordered by step_index
// ascending (step 1 first), for the engine (find the current step's job) and the
// detail API (the step list). A workflow with no jobs yields an empty slice.
func (s *Store) ListWorkflowJobs(id string) ([]JobRecord, error) {
	rows, err := s.db.Query(selectCols+" WHERE workflow_id = ? ORDER BY step_index ASC, id ASC", id)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list workflow jobs %q: %w", id, err)
	}
	defer rows.Close()
	out := make([]JobRecord, 0)
	for rows.Next() {
		rec, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan workflow job row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list workflow jobs %q rows: %w", id, err)
	}
	return out, nil
}
