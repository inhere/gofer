package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Todo lifecycle statuses (Part C §C2, PLAN-02 P2). Done bool is kept in lockstep for
// back-compat: Done ⟺ Status == TodoDone (skipped is terminal but NOT done).
//
// TodoReady sits between pending and doing and is the DISPATCH trigger: a todo that
// is BOTH ready and assigned turns into a job on the spot (job.MaybeDispatchTodo).
// `pending` is the backlog — assigned or not, it never dispatches on its own.
const (
	TodoPending = "pending"
	TodoReady   = "ready"
	TodoDoing   = "doing"
	TodoDone    = "done"
	TodoSkipped = "skipped"
)

// ValidTodoStatus reports whether s is one of the todo lifecycle statuses.
func ValidTodoStatus(s string) bool {
	switch s {
	case TodoPending, TodoReady, TodoDoing, TodoDone, TodoSkipped:
		return true
	}
	return false
}

// PlanTodo is a plan-orchestration todo item. JobID "" is a plain checklist item; a
// non-empty JobID binds it to one job run as metadata. Done is exposed as bool while
// storage stays the 0/1 INTEGER column used by SQLite; Status is the richer lifecycle
// (Part C §C2), StartedAt/DoneAt are stamped automatically on status transitions, Note
// is a short outcome/remark (overwritten, not a process log — that belongs to job
// logs).
//
// The PLAN-02 fields (Assignee … DispatchError) describe HOW and WHERE the item runs
// once it is dispatched: the assignee is the agent, ProjectKey overrides the plan's
// project, and Template/Vars/Verify/Review/Runner/Cwd/TimeoutSec are handed to Submit
// verbatim. DispatchError is the last FAILED attempt's message — a human reads it on
// the checklist instead of digging through a job that was never created; a successful
// dispatch clears it.
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
	// Assignee is the agent key that runs this item ("" = nobody assigned yet, which
	// is why a ready item is not dispatched).
	Assignee string
	// ProjectKey is the project this item runs in, overriding the plan's own.
	ProjectKey string
	// Template is the task book the job's prompt is rendered from ("" = the default
	// plan/todo prompt), Vars its {{variable}} values.
	Template string
	Vars     map[string]string
	// Verify is the argv run after the agent finishes (SUP-01 B), Review asks for a
	// human verdict (GATE-01 S3), Runner/Cwd/TimeoutSec are the job's execution knobs.
	Verify     []string
	Review     bool
	Runner     string
	Cwd        string
	TimeoutSec int
	// DispatchError is why the last dispatch attempt produced no job.
	DispatchError string
	// After lists the plan-todo ids this item WAITS for (PLAN-03): the chain starts
	// it only once every one of them is done or skipped. Empty = a root item, which
	// nothing starts automatically — `plan run` (or a human setting it ready) does.
	After []string
	// Auto allows the chain to start this item automatically once its dependencies
	// are satisfied (default true). False parks it until a human moves it.
	Auto bool
	// Cmd is the argv an `exec` item runs (PLAN-03). Ignored for any other assignee,
	// and required for exec — the dispatcher refuses an exec item without one.
	Cmd       []string
	CreatedAt int64
	UpdatedAt int64
}

// TodoPatch is the set of dispatch fields an update may change (PLAN-02 P2). It is
// shared by every write path — the HTTP PATCH body decodes into it, the CLI and MCP
// fill it, and the store applies it — so the three surfaces cannot drift.
//
// A nil pointer/slice/map means "keep the current value"; a non-nil EMPTY value clears
// it (that is how a runner is taken back or a verify step removed). Status, Note and
// AppendNote are NOT part of it: they move through UpdateTodoStatus/AppendTodoNote,
// which own the lifecycle timestamps.
type TodoPatch struct {
	Assignee   *string           `json:"assignee,omitempty"`
	ProjectKey *string           `json:"project,omitempty"`
	Template   *string           `json:"template,omitempty"`
	Vars       map[string]string `json:"vars,omitempty"`
	Verify     []string          `json:"verify,omitempty"`
	Review     *bool             `json:"review,omitempty"`
	Runner     *string           `json:"runner,omitempty"`
	Cwd        *string           `json:"cwd,omitempty"`
	TimeoutSec *int              `json:"timeout_sec,omitempty"`
	// PLAN-03 chain fields: After sets the dependencies (a non-nil empty list clears
	// them), Auto the auto-advance switch, Cmd the argv of an exec item.
	After *[]string `json:"after,omitempty"`
	Auto  *bool     `json:"auto,omitempty"`
	Cmd   *[]string `json:"cmd,omitempty"`
}

// Empty reports whether the patch would change nothing (the HTTP layer uses it to tell
// an empty update body from a real one).
func (p TodoPatch) Empty() bool {
	return p.Assignee == nil && p.ProjectKey == nil && p.Template == nil && p.Vars == nil &&
		p.Verify == nil && p.Review == nil && p.Runner == nil && p.Cwd == nil && p.TimeoutSec == nil &&
		p.After == nil && p.Auto == nil && p.Cmd == nil
}

const selectTodoCols = `SELECT todo_id, plan_id, COALESCE(job_id,''),
  COALESCE(title,''), COALESCE(done,0), COALESCE(status,''),
  COALESCE(started_at,0), COALESCE(done_at,0), COALESCE(note,''),
  COALESCE(sort,0), COALESCE(assignee,''), COALESCE(project_key,''),
  COALESCE(template,''), COALESCE(vars_json,''), COALESCE(verify_json,''),
  COALESCE(review,0), COALESCE(runner,''), COALESCE(cwd,''),
  COALESCE(timeout_sec,0), COALESCE(dispatch_error,''),
  created_at, updated_at,
  COALESCE(after_json,''), COALESCE(auto,1), COALESCE(cmd_json,'')
  FROM plan_todos`

func scanTodo(sc rowScanner) (PlanTodo, error) {
	var (
		t                  PlanTodo
		done, review, auto int
		varsJSON, verif    string
		afterJSON, cmdJSON string
	)
	err := sc.Scan(&t.TodoID, &t.PlanID, &t.JobID, &t.Title, &done, &t.Status,
		&t.StartedAt, &t.DoneAt, &t.Note, &t.Sort, &t.Assignee, &t.ProjectKey,
		&t.Template, &varsJSON, &verif, &review, &t.Runner, &t.Cwd,
		&t.TimeoutSec, &t.DispatchError, &t.CreatedAt, &t.UpdatedAt,
		&afterJSON, &auto, &cmdJSON)
	if err != nil {
		return PlanTodo{}, err
	}
	t.Done = done != 0
	t.Review = review != 0
	// `auto` defaults to 1 in the schema and in COALESCE, so a row that predates the
	// column reads as "the chain may start this item" — the pre-PLAN-03 behaviour.
	t.Auto = auto != 0
	if t.Vars, err = decodeTodoVars(varsJSON); err != nil {
		return PlanTodo{}, err
	}
	if t.Verify, err = decodeTodoList(verif); err != nil {
		return PlanTodo{}, err
	}
	if t.After, err = decodeTodoList(afterJSON); err != nil {
		return PlanTodo{}, err
	}
	if t.Cmd, err = decodeTodoList(cmdJSON); err != nil {
		return PlanTodo{}, err
	}
	if t.Status == "" {
		// Rows written before the lifecycle columns (or by an old binary racing the
		// migration backfill) surface a status derived from done.
		if t.Done {
			t.Status = TodoDone
		} else {
			t.Status = TodoPending
		}
	}
	return t, nil
}

// decodeTodoVars / decodeTodoVerify read the JSON columns back: an empty column (or an
// empty object/array) is "nothing set" — nil, not an empty non-nil value — so a row
// written by any version reads the same way.
func decodeTodoVars(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("vars_json: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// decodeTodoList reads a JSON []string column back (verify_json, after_json and
// cmd_json share the shape): an empty column (or an empty array) is "nothing set" —
// nil, not an empty non-nil slice — so a row written by any version reads the same way.
func decodeTodoList(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("json list: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// encodeTodoJSON marshals a vars/verify value for storage; a nil or empty value is
// stored as NULL so "not set" stays distinguishable from "set to empty".
func encodeTodoJSON(v any) (any, error) {
	switch t := v.(type) {
	case map[string]string:
		if len(t) == 0 {
			return nil, nil
		}
	case []string:
		if len(t) == 0 {
			return nil, nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
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
	done, review := 0, 0
	if status == TodoDone {
		done = 1
	}
	if t.Review {
		review = 1
	}
	varsVal, err := encodeTodoJSON(t.Vars)
	if err != nil {
		return fmt.Errorf("jobstore: insert todo %q: vars: %w", t.TodoID, err)
	}
	verifyVal, err := encodeTodoJSON(t.Verify)
	if err != nil {
		return fmt.Errorf("jobstore: insert todo %q: verify: %w", t.TodoID, err)
	}
	afterVal, err := encodeTodoJSON(t.After)
	if err != nil {
		return fmt.Errorf("jobstore: insert todo %q: after: %w", t.TodoID, err)
	}
	cmdVal, err := encodeTodoJSON(t.Cmd)
	if err != nil {
		return fmt.Errorf("jobstore: insert todo %q: cmd: %w", t.TodoID, err)
	}
	// The auto flag is written explicitly (never left to the column default) so a todo
	// created with Auto=false is not silently re-armed by the schema's DEFAULT 1.
	auto := 0
	if t.Auto {
		auto = 1
	}
	const q = `INSERT INTO plan_todos
  (todo_id, plan_id, job_id, title, done, status, started_at, done_at, note, sort,
   assignee, project_key, template, vars_json, verify_json, review, runner, cwd,
   timeout_sec, dispatch_error, after_json, auto, cmd_json, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, t.TodoID, t.PlanID, jobID, t.Title, done, status,
		t.StartedAt, t.DoneAt, t.Note, t.Sort, t.Assignee, t.ProjectKey, t.Template,
		varsVal, verifyVal, review, t.Runner, t.Cwd, t.TimeoutSec, t.DispatchError,
		afterVal, auto, cmdVal, t.CreatedAt, t.UpdatedAt); err != nil {
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
		selectTodoCols+" WHERE plan_id = ? ORDER BY sort ASC, created_at ASC, rowid ASC",
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
//   - → ready: the queue position only (PLAN-02 P2) — started_at is untouched (nothing
//     has started), but a re-queued item drops done_at/done so a redone item is not
//     shown as finished;
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
                    WHEN ?1 IN ('doing','pending','ready') THEN 0
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

// SetTodoReadyIfPending promotes a PENDING todo to `ready` in one conditional
// statement and reports whether it was the one that did it (PLAN-03). The
// `WHERE status='pending'` is what makes the chain advance idempotent: two concurrent
// terminal outcomes of the same plan can both decide an item is due, but only one
// flips it, and only that one dispatches it (a second dispatch would be refused by
// the active-job gate anyway — this keeps the event log honest too).
//
// The timestamp rule matches UpdateTodoStatus' `→ ready`: done_at/done are cleared so
// a re-queued item is not shown as finished, started_at is untouched.
func (s *Store) SetTodoReadyIfPending(todoID string) (bool, error) {
	now := s.unixNow()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE plan_todos SET status=?, done=0, done_at=0, updated_at=?
		 WHERE todo_id=? AND status=?`,
		TodoReady, now, todoID, TodoPending,
	)
	if err != nil {
		return false, fmt.Errorf("jobstore: ready todo %q: %w", todoID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AppendTodoNote atomically appends a line to the todo's note (newline-separated;
// empty/NULL note becomes the appended text). Single UPDATE, so concurrent
// appends cannot lose each other's lines.
func (s *Store) AppendTodoNote(todoID, note string) (bool, error) {
	if strings.TrimSpace(note) == "" {
		return false, fmt.Errorf("jobstore: append todo note %q: empty note", todoID)
	}
	now := s.unixNow()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	const q = `UPDATE plan_todos SET
  note       = CASE WHEN note IS NULL OR note = '' THEN ?1 ELSE note || char(10) || ?1 END,
  updated_at = ?2
  WHERE todo_id = ?3`
	res, err := s.db.Exec(q, note, now, todoID)
	if err != nil {
		return false, fmt.Errorf("jobstore: append todo note %q: %w", todoID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UpdateTodoPatch applies the dispatch fields a caller set (PLAN-02 P2): only the
// non-nil entries of p are written, so two writers touching different fields do not
// overwrite each other's value. It returns false (nil error) for an unknown todo, and
// true without writing anything for an empty patch.
func (s *Store) UpdateTodoPatch(todoID string, p TodoPatch) (bool, error) {
	if p.Empty() {
		// Nothing to change: report the todo's existence, touch nothing (updated_at
		// must not move for a no-op update).
		_, ok, err := s.GetTodo(todoID)
		return ok, err
	}
	sets := make([]string, 0, 10)
	args := make([]any, 0, 11)
	add := func(col string, val any) {
		sets = append(sets, col+" = ?")
		args = append(args, val)
	}
	if p.Assignee != nil {
		add("assignee", *p.Assignee)
	}
	if p.ProjectKey != nil {
		add("project_key", *p.ProjectKey)
	}
	if p.Template != nil {
		add("template", *p.Template)
	}
	if p.Vars != nil {
		v, err := encodeTodoJSON(p.Vars)
		if err != nil {
			return false, fmt.Errorf("jobstore: update todo %q vars: %w", todoID, err)
		}
		add("vars_json", v)
	}
	if p.Verify != nil {
		v, err := encodeTodoJSON(p.Verify)
		if err != nil {
			return false, fmt.Errorf("jobstore: update todo %q verify: %w", todoID, err)
		}
		add("verify_json", v)
	}
	if p.Review != nil {
		review := 0
		if *p.Review {
			review = 1
		}
		add("review", review)
	}
	if p.Runner != nil {
		add("runner", *p.Runner)
	}
	if p.Cwd != nil {
		add("cwd", *p.Cwd)
	}
	if p.TimeoutSec != nil {
		add("timeout_sec", *p.TimeoutSec)
	}
	if p.After != nil {
		v, err := encodeTodoJSON(*p.After)
		if err != nil {
			return false, fmt.Errorf("jobstore: update todo %q after: %w", todoID, err)
		}
		add("after_json", v)
	}
	if p.Auto != nil {
		auto := 0
		if *p.Auto {
			auto = 1
		}
		add("auto", auto)
	}
	if p.Cmd != nil {
		v, err := encodeTodoJSON(*p.Cmd)
		if err != nil {
			return false, fmt.Errorf("jobstore: update todo %q cmd: %w", todoID, err)
		}
		add("cmd_json", v)
	}
	add("updated_at", s.unixNow())
	args = append(args, todoID)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec("UPDATE plan_todos SET "+strings.Join(sets, ", ")+" WHERE todo_id = ?", args...)
	if err != nil {
		return false, fmt.Errorf("jobstore: update todo %q patch: %w", todoID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetTodoDispatchError records why the last dispatch attempt produced no job, or
// clears the note with msg "" (PLAN-02 P2). It is the ONE writer of dispatch_error:
// a failed attempt stores the reason, a successful one clears it, and a skipped one
// (the preconditions were simply not met yet) touches nothing. Best-effort by
// contract — the caller dispatches jobs, it does not fail because a note could not be
// written.
func (s *Store) SetTodoDispatchError(todoID, msg string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_todos SET dispatch_error=?, updated_at=? WHERE todo_id=?`,
		msg, s.unixNow(), todoID)
	if err != nil {
		return false, fmt.Errorf("jobstore: set todo %q dispatch error: %w", todoID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ApplyTodoPatch folds a caller's patch into a todo that is about to be inserted (the
// insert-path twin of UpdateTodoPatch): the entry points that CREATE a todo from a
// request body use it so "set on create" and "set on update" cannot drift. A nil entry
// means the field was not sent, so nothing is touched.
func (t *PlanTodo) ApplyTodoPatch(p TodoPatch) {
	if p.Assignee != nil {
		t.Assignee = *p.Assignee
	}
	if p.ProjectKey != nil {
		t.ProjectKey = strings.TrimSpace(*p.ProjectKey)
	}
	if p.Template != nil {
		t.Template = *p.Template
	}
	if p.Vars != nil {
		t.Vars = p.Vars
	}
	if p.Verify != nil {
		t.Verify = p.Verify
	}
	if p.Review != nil {
		t.Review = *p.Review
	}
	if p.Runner != nil {
		t.Runner = *p.Runner
	}
	if p.Cwd != nil {
		t.Cwd = *p.Cwd
	}
	if p.TimeoutSec != nil {
		t.TimeoutSec = *p.TimeoutSec
	}
	if p.After != nil {
		t.After = *p.After
	}
	if p.Auto != nil {
		t.Auto = *p.Auto
	}
	if p.Cmd != nil {
		t.Cmd = *p.Cmd
	}
}

// SetTodoJob binds a todo to the job that most recently carried it (SUP-01 C).
// The todo keeps a pointer to ONE job — the latest run — so the checklist can
// link to "what is doing this" without scanning the jobs table. ok is false when
// the todo is unknown.
func (s *Store) SetTodoJob(todoID, jobID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_todos SET job_id=?, updated_at=? WHERE todo_id=?`,
		jobID, s.unixNow(), todoID)
	if err != nil {
		return false, fmt.Errorf("jobstore: set todo %q job: %w", todoID, err)
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
