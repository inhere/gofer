package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// plan_drivers.go is the persisted state of a plan's DRIVER — whether gofer advances
// the plan by itself, at which autonomy level, and the budget it may spend (design
// docs/design/2026-11-16-plan-driver-autonomy-design.md §1, plan R4).
//
// One row per plan whose driver is ON; no row means OFF, which is what makes an upgrade
// turn nothing on (Q1-A: a plan opts in with `plan drive on`). The row is deliberately
// thin — level plus two JSON blobs:
//
//	level        the autonomy level, stored verbatim (the job layer is the ONE validator;
//	             jobstore stays value-permissive like the other plan setters). This batch
//	             accepts `record` only.
//	budget_json  the per-plan budget the driver counts against (job.PlanDriverBudget).
//	spent_json   what it has spent so far (the driver's own accounting).
//
// Nothing here parses the two blobs, so this package keeps its single rule: no
// dependency on internal/job.
//
// The row dies with its plan: SetPlanStatus deletes it in the SAME transaction when a
// plan is archived (this store has no plan delete of its own), so an archived plan never
// comes back with a stale driver.

// planDriverSchema is additive (IF NOT EXISTS) and appended to schemaStmts below, so the
// table lands on fresh and existing databases alike. G057: nothing here refers to a
// column a migration adds.
var planDriverSchema = []string{
	`CREATE TABLE IF NOT EXISTS plan_drivers (
  plan_id     TEXT PRIMARY KEY,
  level       TEXT NOT NULL,
  budget_json TEXT NOT NULL DEFAULT '{}',
  spent_json  TEXT NOT NULL DEFAULT '{}',
  enabled_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
)`,
}

func init() { schemaStmts = append(schemaStmts, planDriverSchema...) }

// PlanDriver is one row of plan_drivers: the driver state of one plan. It is neutral
// (no internal/job import, like every other type here): BudgetJSON / SpentJSON are the
// blobs the driver wrote, handed back unchanged.
type PlanDriver struct {
	PlanID string
	Level  string
	// BudgetJSON is the budget the driver counts against ("{}" when none was set).
	BudgetJSON string
	// SpentJSON is what the driver has spent so far ("{}" when it spent nothing).
	SpentJSON string
	// EnabledAt is when the driver was enabled (the start of its accounting window); an
	// update rewrites it, so re-writing the same state restarts the window.
	EnabledAt int64
	UpdatedAt int64
}

const selectPlanDriverCols = `SELECT plan_id, level, COALESCE(budget_json,'{}'),
  COALESCE(spent_json,'{}'), enabled_at, updated_at FROM plan_drivers`

func scanPlanDriver(sc rowScanner) (PlanDriver, error) {
	var d PlanDriver
	err := sc.Scan(&d.PlanID, &d.Level, &d.BudgetJSON, &d.SpentJSON, &d.EnabledAt, &d.UpdatedAt)
	return d, err
}

// UpsertPlanDriver writes (replaces) the driver state of one plan. A half-built row (no
// plan id or no level) is refused: a driver nobody can name cannot be switched off.
// Everything given is what the row holds afterwards — turning a driver on (or changing
// its level or budget) starts a fresh accounting window, which is also why EnabledAt
// defaults to now and the two blobs to "{}".
func (s *Store) UpsertPlanDriver(d PlanDriver) error {
	if d.PlanID == "" || d.Level == "" {
		return errors.New("jobstore: UpsertPlanDriver needs plan_id and level")
	}
	if d.BudgetJSON == "" {
		d.BudgetJSON = "{}"
	}
	if d.SpentJSON == "" {
		d.SpentJSON = "{}"
	}
	now := s.unixNow()
	if d.UpdatedAt == 0 {
		d.UpdatedAt = now
	}
	if d.EnabledAt == 0 {
		d.EnabledAt = now
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO plan_drivers
  (plan_id, level, budget_json, spent_json, enabled_at, updated_at) VALUES (?,?,?,?,?,?)
  ON CONFLICT(plan_id) DO UPDATE SET level=excluded.level, budget_json=excluded.budget_json,
  spent_json=excluded.spent_json, enabled_at=excluded.enabled_at, updated_at=excluded.updated_at`,
		d.PlanID, d.Level, d.BudgetJSON, d.SpentJSON, d.EnabledAt, d.UpdatedAt)
	if err != nil {
		return fmt.Errorf("jobstore: upsert plan driver %q: %w", d.PlanID, err)
	}
	return nil
}

// GetPlanDriver reads one plan's driver state; ok=false (nil error) means the driver is
// off for it.
func (s *Store) GetPlanDriver(planID string) (PlanDriver, bool, error) {
	d, err := scanPlanDriver(s.db.QueryRow(selectPlanDriverCols+" WHERE plan_id=?", planID))
	if errors.Is(err, sql.ErrNoRows) {
		return PlanDriver{}, false, nil
	}
	if err != nil {
		return PlanDriver{}, false, fmt.Errorf("jobstore: get plan driver %q: %w", planID, err)
	}
	return d, true, nil
}

// DeletePlanDriver turns a plan's driver off and reports whether a row was there (an
// already-off driver is not an error: `plan drive off` is idempotent).
func (s *Store) DeletePlanDriver(planID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM plan_drivers WHERE plan_id=?`, planID)
	if err != nil {
		return false, fmt.Errorf("jobstore: delete plan driver %q: %w", planID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: delete plan driver %q rows: %w", planID, err)
	}
	return n > 0, nil
}

// ListPlanDrivers returns every enabled driver, oldest first (the order they were turned
// on in — what a sweep that replays them wants).
func (s *Store) ListPlanDrivers() ([]PlanDriver, error) {
	rows, err := s.db.Query(selectPlanDriverCols + " ORDER BY enabled_at ASC, plan_id ASC")
	if err != nil {
		return nil, fmt.Errorf("jobstore: list plan drivers: %w", err)
	}
	defer rows.Close()
	out := make([]PlanDriver, 0)
	for rows.Next() {
		d, err := scanPlanDriver(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan plan driver: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
