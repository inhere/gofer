package jobstore

import (
	"database/sql"
	"fmt"
)

// plan_driver_actions.go is the action log of a plan's DRIVER (design
// docs/design/2026-11-16-plan-driver-autonomy-design.md §1, plan R5): one row per action the
// driver WOULD take at a plan event (record level never executes it), later stamped with
// what actually happened (outcome, plan R7). The table is new and additive (G057).
//
// The name is deliberately not `plan_decisions`: that table is the question / answer
// channel of `plan ask`. Nothing here parses params_json; the driver package owns the
// vocabulary of actions, rules, risks and outcomes, and this package only stores strings.
//
// Rows die with their plan: SetPlanStatus deletes them in the same transaction as the
// driver row when a plan is archived.

var planDriverActionSchema = []string{
	`CREATE TABLE IF NOT EXISTS plan_driver_actions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, plan_id TEXT NOT NULL, todo_id TEXT NOT NULL DEFAULT '',
 job_id TEXT NOT NULL DEFAULT '', event_kind TEXT NOT NULL DEFAULT '', action TEXT NOT NULL,
 params_json TEXT NOT NULL DEFAULT '{}', risk TEXT NOT NULL, rule TEXT NOT NULL,
 would_pause INTEGER NOT NULL DEFAULT 0, outcome TEXT NOT NULL DEFAULT '',
 matched_at INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_plan_driver_actions_plan ON plan_driver_actions(plan_id, id)`,
}

func init() { schemaStmts = append(schemaStmts, planDriverActionSchema...) }

// PlanDriverAction is one row of plan_driver_actions. Outcome "" = not observed yet.
type PlanDriverAction struct {
	ID        int64
	PlanID    string
	TodoID    string
	JobID     string
	EventKind string
	Action    string
	// ParamsJSON is the action's parameters and the facts the rule decided on.
	ParamsJSON string
	Risk       string
	Rule       string
	WouldPause bool
	Outcome    string
	// MatchedAt is when Outcome was stamped (unix seconds, 0 while unobserved).
	MatchedAt int64
	CreatedAt int64
}

const selectPlanDriverActionCols = `SELECT id, plan_id, todo_id, job_id, event_kind, action,
  params_json, risk, rule, would_pause, outcome, matched_at, created_at FROM plan_driver_actions`

func scanPlanDriverAction(sc rowScanner) (PlanDriverAction, error) {
	var a PlanDriverAction
	var wp int
	err := sc.Scan(&a.ID, &a.PlanID, &a.TodoID, &a.JobID, &a.EventKind, &a.Action,
		&a.ParamsJSON, &a.Risk, &a.Rule, &wp, &a.Outcome, &a.MatchedAt, &a.CreatedAt)
	a.WouldPause = wp != 0
	return a, err
}

// InsertPlanDriverAction stores one action and returns its id. A row without a plan, an
// action, a risk or a rule is refused (nothing could explain it later). CreatedAt defaults
// to now, ParamsJSON to "{}".
func (s *Store) InsertPlanDriverAction(a PlanDriverAction) (int64, error) {
	if a.PlanID == "" || a.Action == "" || a.Risk == "" || a.Rule == "" {
		return 0, fmt.Errorf("jobstore: InsertPlanDriverAction needs plan_id, action, risk and rule")
	}
	if a.ParamsJSON == "" {
		a.ParamsJSON = "{}"
	}
	if a.CreatedAt == 0 {
		a.CreatedAt = s.unixNow()
	}
	wp := 0
	if a.WouldPause {
		wp = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO plan_driver_actions
  (plan_id, todo_id, job_id, event_kind, action, params_json, risk, rule, would_pause, outcome, matched_at, created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.PlanID, a.TodoID, a.JobID, a.EventKind, a.Action, a.ParamsJSON, a.Risk, a.Rule, wp,
		a.Outcome, a.MatchedAt, a.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("jobstore: insert plan driver action: %w", err)
	}
	return res.LastInsertId()
}

// ListPlanDriverActions returns a plan's actions oldest first. unresolvedOnly keeps just the
// rows whose outcome is not stamped yet.
func (s *Store) ListPlanDriverActions(planID string, unresolvedOnly bool) ([]PlanDriverAction, error) {
	q := selectPlanDriverActionCols + ` WHERE plan_id=?`
	if unresolvedOnly {
		q += ` AND outcome=''`
	}
	rows, err := s.db.Query(q+` ORDER BY id`, planID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list plan driver actions %q: %w", planID, err)
	}
	defer rows.Close()
	out := make([]PlanDriverAction, 0)
	for rows.Next() {
		a, err := scanPlanDriverAction(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan plan driver action: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ResolvePlanDriverAction stamps the observed outcome on an action that has none yet, and
// reports whether it did (a row already resolved, or gone, is left as it is — observation
// may run from two places at once).
func (s *Store) ResolvePlanDriverAction(id int64, outcome string, matchedAt int64) (bool, error) {
	if outcome == "" {
		return false, fmt.Errorf("jobstore: ResolvePlanDriverAction needs an outcome")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_driver_actions SET outcome=?, matched_at=? WHERE id=? AND outcome=''`,
		outcome, matchedAt, id)
	if err != nil {
		return false, fmt.Errorf("jobstore: resolve plan driver action %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: resolve plan driver action %d rows: %w", id, err)
	}
	return n > 0, nil
}

// UpdatePlanDriverSpent writes the driver's accounting blob without touching the rest of
// the row (level, budget and the accounting window stay as the human set them), and
// reports whether the driver row exists.
func (s *Store) UpdatePlanDriverSpent(planID, spentJSON string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_drivers SET spent_json=? WHERE plan_id=?`, spentJSON, planID)
	if err != nil {
		return false, fmt.Errorf("jobstore: update plan driver spent %q: %w", planID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: update plan driver spent %q rows: %w", planID, err)
	}
	return n > 0, nil
}

// PlanJobSpend is what a plan's jobs have used since a point in time.
type PlanJobSpend struct {
	// Jobs counts the jobs the plan's items started (continuations and retries are jobs too).
	Jobs int
	// CheckResumes sums the delivery-check continuation rounds those jobs recorded.
	CheckResumes int
}

// PlanJobSpendSince counts the jobs carried by a plan's items that were queued at or after
// since (unix seconds), and the delivery-check resumes they recorded. A job's queue time
// falls back to its start time for rows written before queued_at existed.
func (s *Store) PlanJobSpendSince(planID string, since int64) (PlanJobSpend, error) {
	var sp PlanJobSpend
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(check_resume_count),0) FROM jobs
  WHERE todo_id IN (SELECT todo_id FROM plan_todos WHERE plan_id=?)
  AND COALESCE(NULLIF(queued_at,0), started_at, 0) >= ?`, planID, since).Scan(&sp.Jobs, &sp.CheckResumes)
	if err != nil && err != sql.ErrNoRows {
		return PlanJobSpend{}, fmt.Errorf("jobstore: plan job spend %q: %w", planID, err)
	}
	return sp, nil
}
