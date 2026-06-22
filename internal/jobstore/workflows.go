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
	// StepAttempt is the 1-based attempt of the CURRENT step (P1, design §5.2).
	// First run == 1; a retry advances (current_step,step_attempt) from (cur,att) to
	// (cur,att+1). Together with CurrentStep it forms the二元组 AdvanceStep抢权 on
	// (旧库/v1 行 COALESCE→1). 推进到下一步时 step_attempt 重置为 1.
	StepAttempt int
	// NextStepAt is the退避到点时间 (unix seconds) before the current step's next
	// attempt may start (P1, design §5.2). 0 == immediate (no backoff pending). Set
	// by a retry transition; the sweeper/advance skip a workflow whose NextStepAt is
	// still in the future, then start the next attempt once it is due. 旧库行→0.
	NextStepAt int64
}

// selectWorkflowCols is the shared projection. COALESCE guards the nullable
// title/caller_id/error so a NULL scans into "" instead of failing the scan. The
// P1 step_attempt/next_step_at columns COALESCE旧库行 into the v1-equivalent zero
// value (attempt 1, no pending backoff) so a pre-existing workflow scans cleanly.
const selectWorkflowCols = `SELECT id, COALESCE(title,''), status, current_step,
  total_steps, spec_json, COALESCE(caller_id,''), COALESCE(error,''),
  created_at, updated_at,
  COALESCE(step_attempt,1), COALESCE(next_step_at,0) FROM workflows`

// scanWorkflow reads one row (in selectWorkflowCols order) into a Workflow.
func scanWorkflow(sc rowScanner) (Workflow, error) {
	var w Workflow
	err := sc.Scan(
		&w.ID, &w.Title, &w.Status, &w.CurrentStep,
		&w.TotalSteps, &w.SpecJSON, &w.CallerID, &w.Error,
		&w.CreatedAt, &w.UpdatedAt,
		&w.StepAttempt, &w.NextStepAt,
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
	if w.StepAttempt < 1 {
		w.StepAttempt = 1 // 默认首个 step 的 attempt=1（P1）
	}
	const q = `INSERT INTO workflows
  (id, title, status, current_step, total_steps, spec_json, caller_id, error, created_at, updated_at, step_attempt, next_step_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q,
		w.ID, w.Title, w.Status, w.CurrentStep, w.TotalSteps,
		w.SpecJSON, w.CallerID, w.Error, w.CreatedAt, w.UpdatedAt,
		w.StepAttempt, w.NextStepAt,
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

// AdvanceStep is the推进幂等屏障 (SR303), upgraded for工作流 v2 retry (P1, plan
// ⭐节 2). It atomically moves a running workflow's (current_step, step_attempt)
// 二元组 from (fromStep,fromAtt) to (toStep,toAtt) via a conditional UPDATE, sets
// next_step_at (退避到点时间; 0=immediate), and returns true ONLY for the UPDATE
// that actually changed the row.
//
// The WHERE re-checks current_step==fromStep AND step_attempt==fromAtt AND
// status=='running', so any two concurrent/repeated callers (the finish hook + the
// sweeper firing for the SAME finished (step,attempt)) can never both win: the
// first moves the二元组 off (fromStep,fromAtt), the second affects 0 rows and
// returns false. Every v2 transition is the same symmetric抢权:
//   - 推进下一步: (cur,att) → (cur+1, 1),       nextStepAt=0
//   - 重试本步:   (cur,att) → (cur,   att+1),   nextStepAt=now+backoff
//   - 继续(跳过): (cur,att) → (cur+1, 1),       nextStepAt=0
//
// so the赢家唯一 invariant ("一个 (step,attempt) 的状态转移绝不执行两次") holds
// for推进/重试/继续 alike. Layered on the deterministic request_id (⭐节 1) this is
// the double-safeguard that no (step,attempt) ever starts two jobs. Runs under
// writeMu (every writer's in-process lock).
func (s *Store) AdvanceStep(id string, fromStep, fromAtt, toStep, toAtt int, nextStepAt int64) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE workflows SET current_step = ?, step_attempt = ?, next_step_at = ?, updated_at = ?
     WHERE id = ? AND current_step = ? AND step_attempt = ? AND status = ?`,
		toStep, toAtt, nextStepAt, s.unixNow(), id, fromStep, fromAtt, WorkflowRunning,
	)
	if err != nil {
		return false, fmt.Errorf("jobstore: advance workflow %q (%d,%d)->(%d,%d): %w", id, fromStep, fromAtt, toStep, toAtt, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AdvanceCurrentStep is the v1推进屏障, retained as a thin AdvanceStep wrapper for
// the existing direct-claim tests and any single-attempt推进 (the workflow engine
// itself now calls AdvanceStep with the explicit二元组). It moves current_step from
// `from` to `to` for attempt 1, leaving the attempt at 1 and no pending backoff —
// equivalent to the v1 single-value conditional UPDATE. New code should call
// AdvanceStep directly with the (step,attempt)二元组.
func (s *Store) AdvanceCurrentStep(id string, from, to int) (bool, error) {
	return s.AdvanceStep(id, from, 1, to, 1, 0)
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
