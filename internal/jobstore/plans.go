package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Plan status values. A plan is a lightweight grouping header; it does not
// advance jobs itself — except through the PLAN-03 chain advance, which only ever
// moves it to PlanDone (all items finished) or PlanBlocked (a chain job failed).
const (
	PlanIDMinLength = 9
	PlanOpen        = "open"
	PlanActive      = "active"
	PlanDone        = "done"
	PlanArchived    = "archived"
	// PlanBlocked is NOT terminal: a failed chain job parked the plan, and a human
	// releasing the item (set it ready/skipped) or `plan run|resume` puts it back to
	// PlanOpen and continues the chain.
	PlanBlocked = "blocked"
)

// Plan leader switch values (LEAD-02). `leader` is a STRING on the plan rather than a
// bool so the zero value (an old row, or a caller that never set it) is the safe
// default: `off`. The global supervisor.leader block only carries the parameters
// (agent/delay/rounds) and the master switch, so a plan that wants rounds must opt in.
const (
	PlanLeaderOff = "off"
	PlanLeaderOn  = "on"
)

// ValidPlanLeader reports whether s is a value the leader switch accepts.
func ValidPlanLeader(s string) bool { return s == PlanLeaderOff || s == PlanLeaderOn }

// Plan is the SQLite-persisted plan grouping header. It is neutral (no
// internal/job import) so job/http layers can drive it without an import cycle.
type Plan struct {
	PlanID      string
	Title       string
	Description string
	Status      string
	Owner       string
	Progress    int
	// ProjectKey is the project this plan's todos are dispatched INTO (PLAN-02 P2,
	// `plan create --project`). It is what makes "指派即派发" a one-liner: a todo
	// without its own project_key runs in the plan's. Empty = the plan names none, so
	// every todo must carry its own (and a dispatch without one is refused).
	ProjectKey string
	// Paused holds the automatic chain advance (PLAN-03): a todo that finishes while
	// the plan is paused does NOT start its dependents.
	Paused bool
	// BlockedTodo is the item a FAILED chain job parked the plan on ("" = not
	// blocked). Status is PlanBlocked while it is set.
	BlockedTodo string
	// Leader is this plan's leader-round switch (LEAD-02): PlanLeaderOn arms a round
	// when a member job of the plan finishes, PlanLeaderOff (the default) does not.
	// The global supervisor.leader block stays the master switch; this one decides
	// WHICH plans the rounds run for.
	Leader    string
	CreatedAt int64
	UpdatedAt int64
}

const selectPlanCols = `SELECT plan_id, COALESCE(title,''), COALESCE(description,''),
  status, COALESCE(owner,''), COALESCE(progress,0), COALESCE(project_key,''),
  COALESCE(paused,0), COALESCE(blocked_todo,''), COALESCE(leader,'off'),
  created_at, updated_at FROM plans`

func scanPlan(sc rowScanner) (Plan, error) {
	var (
		p      Plan
		paused int
	)
	err := sc.Scan(&p.PlanID, &p.Title, &p.Description, &p.Status, &p.Owner,
		&p.Progress, &p.ProjectKey, &paused, &p.BlockedTodo, &p.Leader, &p.CreatedAt, &p.UpdatedAt)
	p.Paused = paused != 0
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
	paused := 0
	if p.Paused {
		paused = 1
	}
	// The switch defaults to off on the way in: a caller that never mentions the leader
	// round must not opt a plan into it by leaving a zero string behind.
	if !ValidPlanLeader(p.Leader) {
		p.Leader = PlanLeaderOff
	}
	const q = `INSERT INTO plans
  (plan_id, title, description, status, owner, progress, project_key, paused, blocked_todo, leader, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, p.PlanID, p.Title, p.Description, p.Status, p.Owner,
		p.Progress, p.ProjectKey, paused, p.BlockedTodo, p.Leader, p.CreatedAt, p.UpdatedAt); err != nil {
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

// Plan page sizes (F-d): what a caller that names no limit gets, and the ceiling one
// page may ask for — a 10k-row request must not make the server build a 10k-row page
// (and the CLI's --all pages through instead).
const (
	PlanListDefaultLimit = 20
	PlanListMaxLimit     = 100
)

// NormalizePlanLimit turns a caller-supplied page size into the one the store uses:
// <=0 becomes PlanListDefaultLimit, anything above PlanListMaxLimit is clamped.
func NormalizePlanLimit(n int) int {
	switch {
	case n <= 0:
		return PlanListDefaultLimit
	case n > PlanListMaxLimit:
		return PlanListMaxLimit
	}
	return n
}

// PlanFilter selects and pages the plan list. The zero value means "every plan, newest
// first, one default-sized page".
type PlanFilter struct {
	// Status restricts to one plan status ("" = any).
	Status string
	// ProjectKey restricts to plans created for that project ("" = any), exact match.
	ProjectKey string
	// Q matches the plan id by PREFIX or the title by SUBSTRING, case-insensitively
	// ("" = any). LIKE's own wildcards are literal here: a `%` in the query searches for
	// a percent sign, it does not match everything.
	Q string
	// Limit caps one page (see NormalizePlanLimit). Offset skips rows of the filtered,
	// newest-first list.
	Limit  int
	Offset int
}

// planWhere renders the filter's WHERE clause and args, shared by ListPlans and
// CountPlans so a page and its total can never disagree about what "matching" means.
func (f PlanFilter) planWhere() (string, []any) {
	var conds []string
	var args []any
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.ProjectKey != "" {
		conds = append(conds, "project_key = ?")
		args = append(args, f.ProjectKey)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		// SQLite's LIKE is already ASCII case-insensitive, so LOWER() is only needed to
		// keep the contract explicit (and true for a future non-SQLite store).
		esc := escapeLikePattern(strings.ToLower(q))
		conds = append(conds, `(LOWER(plan_id) LIKE ? ESCAPE '\' OR LOWER(COALESCE(title,'')) LIKE ? ESCAPE '\')`)
		args = append(args, esc+"%", "%"+esc+"%")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// escapeLikePattern escapes the LIKE metacharacters in a user-supplied query so they
// match literally (paired with `ESCAPE '\'`).
func escapeLikePattern(s string) string {
	if !strings.ContainsAny(s, `\%_`) {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ListPlans returns one page of plans matching f, newest first. The order is
// created_at DESC, rowid DESC — a total order, so consecutive pages neither repeat nor
// skip a row that shares a created_at with its neighbour.
func (s *Store) ListPlans(f PlanFilter) ([]Plan, error) {
	where, args := f.planWhere()
	query := selectPlanCols + where + " ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?"
	args = append(args, NormalizePlanLimit(f.Limit), max(f.Offset, 0))

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

// CountPlans counts every plan matching f — the same conditions as ListPlans, without
// paging. It is the `total` a paging UI needs ("showing 1–20 of 57").
func (s *Store) CountPlans(f PlanFilter) (int, error) {
	where, args := f.planWhere()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM plans"+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("jobstore: count plans: %w", err)
	}
	return n, nil
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

// SetPlanPaused holds or releases a plan's automatic chain advance (PLAN-03).
// progress is untouched.
func (s *Store) SetPlanPaused(id string, paused bool) error {
	v := 0
	if paused {
		v = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE plans SET paused=?, updated_at=? WHERE plan_id=?`,
		v, s.unixNow(), id); err != nil {
		return fmt.Errorf("jobstore: set plan %q paused: %w", id, err)
	}
	return nil
}

// SetPlanLeader flips a plan's leader-round switch (LEAD-02). It touches nothing else:
// the switch is a human's decision about a plan, not a status change. The caller
// validates the value at its own boundary (jobstore stays value-permissive like the
// other plan setters).
func (s *Store) SetPlanLeader(id, leader string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE plans SET leader = ?, updated_at = ? WHERE plan_id = ?`,
		leader, s.unixNow(), id); err != nil {
		return fmt.Errorf("jobstore: set plan leader %q: %w", id, err)
	}
	return nil
}

// SetPlanBlocked parks a plan on the item a failed chain job belongs to (PLAN-03):
// blocked_todo records WHICH item, and the status becomes PlanBlocked so the plan
// list and the web banner show it. The two move in ONE statement — a plan whose
// status says blocked but names no item would be unactionable.
func (s *Store) SetPlanBlocked(id, todoID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE plans SET status=?, blocked_todo=?, updated_at=? WHERE plan_id=?`,
		PlanBlocked, todoID, s.unixNow(), id,
	); err != nil {
		return fmt.Errorf("jobstore: set plan %q blocked on %q: %w", id, todoID, err)
	}
	return nil
}

// ClearPlanBlocked releases a plan from a block: blocked_todo is emptied and a
// PlanBlocked status returns to PlanOpen. Any OTHER status (active, done, archived —
// a human's own choice) is left alone, and a plan that is not blocked is unchanged
// apart from updated_at. Idempotent: the unblock paths all call it unconditionally.
func (s *Store) ClearPlanBlocked(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE plans SET blocked_todo='',
		   status=CASE WHEN status=? THEN ? ELSE status END, updated_at=?
		 WHERE plan_id=?`,
		PlanBlocked, PlanOpen, s.unixNow(), id,
	); err != nil {
		return fmt.Errorf("jobstore: clear plan %q blocked: %w", id, err)
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

// add counts n todos of status st (an unknown/legacy status counts as pending, and so
// does `ready` — a queued item is unfinished, and the P2 status adds no bucket the
// plan card would have to learn).
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

// PlanUsage is a plan's token/cost roll-up (PLAN-02 P2): what the jobs attached to it
// reported, in total and per agent. Jobs counts EVERY attached job — one that captured
// no usage still ran — while the sums only see the rows that reported numbers, exactly
// like UsageWindow (see usageStatsQuery for why the extraction is json_valid-guarded).
type PlanUsage struct {
	Jobs        int
	TotalTokens int64
	CostUSD     float64
	// ByAgent is keyed by agent key; a job with an empty agent is keyed by "".
	ByAgent map[string]UsageAgent
}

// planUsageQuery is usageStatsQuery scoped to ONE plan: the same columns, the same
// guards, so the plan card and the dashboard can never disagree about a job.
const planUsageQuery = `SELECT agent, COUNT(*),
  COALESCE(SUM(CASE WHEN json_valid(usage_json) THEN json_extract(usage_json,'$.total_tokens') END),0),
  COALESCE(SUM(CASE WHEN json_valid(usage_json) THEN json_extract(usage_json,'$.input_tokens') END),0),
  COALESCE(SUM(CASE WHEN json_valid(usage_json) THEN json_extract(usage_json,'$.output_tokens') END),0),
  COALESCE(SUM(CASE WHEN json_valid(usage_json) THEN json_extract(usage_json,'$.cost_usd') END),0)
FROM jobs WHERE plan_id = ? GROUP BY agent`

// PlanUsage aggregates the usage its attached jobs reported, per agent and in total.
// A plan with no jobs (or none that reported usage) is the zero value — ByAgent is
// always non-nil so a caller can index it without a nil check.
func (s *Store) PlanUsage(planID string) (PlanUsage, error) {
	rows, err := s.db.Query(planUsageQuery, planID)
	if err != nil {
		return PlanUsage{}, fmt.Errorf("jobstore: plan usage %q: %w", planID, err)
	}
	defer func() { _ = rows.Close() }()

	out := PlanUsage{ByAgent: map[string]UsageAgent{}}
	for rows.Next() {
		var (
			agent string
			a     UsageAgent
		)
		if err := rows.Scan(&agent, &a.Jobs, &a.TotalTokens, &a.InputTokens, &a.OutputTokens, &a.CostUSD); err != nil {
			return PlanUsage{}, fmt.Errorf("jobstore: scan plan usage row: %w", err)
		}
		out.ByAgent[agent] = a
		out.Jobs += a.Jobs
		out.TotalTokens += a.TotalTokens
		out.CostUSD += a.CostUSD
	}
	if err := rows.Err(); err != nil {
		return PlanUsage{}, fmt.Errorf("jobstore: plan usage rows %q: %w", planID, err)
	}
	return out, nil
}
