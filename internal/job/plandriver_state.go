package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// Plan driver state (design docs/design/2026-11-16-plan-driver-autonomy-design.md §1,
// plan R4): whether gofer advances a plan by itself, at which autonomy level, and with
// which budget. This file owns the STATE AND ITS SWITCHES only — the rules that decide an
// action, the action log and the agreement report are the driver package's job.
//
// A driver is OFF until a human turns it on (`gofer plan drive on`), and this batch
// accepts the `record` level only: record observes and logs what the driver WOULD have
// done without doing it, which is what P2 measures. `assist` / `auto` are refused with an
// explicit "later version" message rather than silently downgraded, so nobody believes an
// automatic plan is being driven when nothing acts.

// Autonomy levels of a plan driver (design §1 「放权等级」).
const (
	// PlanDriverLevelRecord only records the action the driver would take.
	PlanDriverLevelRecord = "record"
	// PlanDriverLevelAssist would act on low-risk actions and ask a human otherwise.
	PlanDriverLevelAssist = "assist"
	// PlanDriverLevelAuto would act inside its budget, asking a human only for red lines.
	PlanDriverLevelAuto = "auto"
)

// PlanDriverInvalid is the sentinel of a driver request the caller must fix — an unknown
// level, a level this version does not support, a negative budget. The HTTP layer answers
// 400 for it (writePlanDriverError), and the CLI prints the message as it is.
var PlanDriverInvalid = errors.New("invalid plan driver request")

// ErrPlanNotFound is returned by the plan helpers that must tell "no such plan" apart
// from a store failure. The HTTP layer maps it to 404 (the plan actions keep their own
// road: they wrap ErrInvalidRequest, which planActionStatus reads as an unknown plan).
var ErrPlanNotFound = errors.New("unknown plan")

// PlanDriverBudget is a plan driver's budget (plan R8). Every field is a ceiling for the
// whole plan since the driver was enabled; 0 means unlimited, so a zero value is the
// widest budget rather than the narrowest — the safe reading for a row written before the
// field existed.
type PlanDriverBudget struct {
	// MaxJobs caps the jobs the plan's items may start (rework and retries included).
	MaxJobs int `json:"max_jobs"`
	// MaxRework caps the rework rounds (resume-with-notes plus delivery-check continuations).
	MaxRework int `json:"max_rework"`
	// MaxHours caps the wall-clock hours since the driver was enabled.
	MaxHours float64 `json:"max_hours"`
}

// PlanDriverStatus is the answer of `gofer plan drive status` and GET
// /v1/plans/{id}/driver. Field names are part of the HTTP contract.
type PlanDriverStatus struct {
	PlanID string `json:"plan_id"`
	// Enabled says whether the driver is on; a plan without a driver row reports false
	// with an empty level and an all-unlimited budget.
	Enabled bool             `json:"enabled"`
	Level   string           `json:"level,omitempty"`
	Budget  PlanDriverBudget `json:"budget"`
	// EnabledAt / UpdatedAt are unix seconds (0 when the driver is off).
	EnabledAt int64 `json:"enabled_at,omitempty"`
	UpdatedAt int64 `json:"updated_at,omitempty"`
}

// EnablePlanDriver turns a plan's driver on at level with budget. level "" means the
// default `record`. An unknown plan is ErrPlanNotFound, a level this version does not
// support (or an unknown one) and a negative / non-finite budget are PlanDriverInvalid
// — every refusal happens BEFORE anything is written.
func (s *Service) EnablePlanDriver(planID, level string, budget PlanDriverBudget) error {
	p, ok, err := s.meta.GetPlan(planID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: unknown plan %q", ErrPlanNotFound, planID)
	}
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		level = PlanDriverLevelRecord
	}
	lang := s.notifyLang(p.ProjectKey)
	switch level {
	case PlanDriverLevelRecord:
	case PlanDriverLevelAssist, PlanDriverLevelAuto:
		return fmt.Errorf("%w: %s", PlanDriverInvalid,
			i18n.T(lang, "job.plan_driver.level_later", "level", level))
	default:
		return fmt.Errorf("%w: %s", PlanDriverInvalid,
			i18n.T(lang, "job.plan_driver.level_unknown", "level", level))
	}
	if err := checkPlanDriverBudget(lang, budget); err != nil {
		return err
	}
	blob, err := json.Marshal(budget)
	if err != nil {
		return fmt.Errorf("encode plan driver budget: %w", err)
	}
	if err := s.meta.UpsertPlanDriver(jobstore.PlanDriver{
		PlanID: planID, Level: level, BudgetJSON: string(blob),
	}); err != nil {
		return err
	}
	slog.Info("plan driver enabled", "plan_id", planID, "level", level,
		"max_jobs", budget.MaxJobs, "max_rework", budget.MaxRework, "max_hours", budget.MaxHours)
	return nil
}

// checkPlanDriverBudget refuses a budget no driver could honour: a negative ceiling (0 is
// the "unlimited" value, so a negative one is a typo, never a smaller limit) and an hours
// ceiling that is not a finite number (it would not even encode as JSON).
func checkPlanDriverBudget(lang i18n.Lang, b PlanDriverBudget) error {
	for _, f := range []struct {
		name string
		v    int
	}{{"max_jobs", b.MaxJobs}, {"max_rework", b.MaxRework}} {
		if f.v < 0 {
			return fmt.Errorf("%w: %s", PlanDriverInvalid,
				i18n.T(lang, "job.plan_driver.budget_negative", "field", f.name))
		}
	}
	if b.MaxHours < 0 || math.IsNaN(b.MaxHours) || math.IsInf(b.MaxHours, 0) {
		return fmt.Errorf("%w: %s", PlanDriverInvalid,
			i18n.T(lang, "job.plan_driver.budget_not_finite", "field", "max_hours"))
	}
	return nil
}

// DisablePlanDriver turns a plan's driver off. Turning an already-off driver off is not an
// error (`plan drive off` is idempotent — a script can ask for the state it wants); an
// unknown plan is ErrPlanNotFound.
func (s *Service) DisablePlanDriver(planID string) error {
	if _, ok, err := s.meta.GetPlan(planID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: unknown plan %q", ErrPlanNotFound, planID)
	}
	if _, err := s.meta.DeletePlanDriver(planID); err != nil {
		return err
	}
	slog.Info("plan driver disabled", "plan_id", planID)
	return nil
}

// PlanDriverStatus reports a plan's driver state. A plan without a driver row reports
// Enabled=false (nothing was ever turned on) rather than a 404; an unknown plan is
// ErrPlanNotFound.
func (s *Service) PlanDriverStatus(planID string) (PlanDriverStatus, error) {
	if _, ok, err := s.meta.GetPlan(planID); err != nil {
		return PlanDriverStatus{}, err
	} else if !ok {
		return PlanDriverStatus{}, fmt.Errorf("%w: unknown plan %q", ErrPlanNotFound, planID)
	}
	d, ok, err := s.meta.GetPlanDriver(planID)
	if err != nil {
		return PlanDriverStatus{}, err
	}
	if !ok {
		return PlanDriverStatus{PlanID: planID}, nil
	}
	return PlanDriverStatus{
		PlanID:    planID,
		Enabled:   true,
		Level:     d.Level,
		Budget:    parsePlanDriverBudget(d.BudgetJSON),
		EnabledAt: d.EnabledAt,
		UpdatedAt: d.UpdatedAt,
	}, nil
}

// parsePlanDriverBudget reads the stored budget blob. A blob gofer itself cannot read back
// (written by a newer version, or corrupted) reads as the zero budget — unlimited — since
// the alternative is reporting a limit nobody set.
func parsePlanDriverBudget(raw string) PlanDriverBudget {
	var b PlanDriverBudget
	if strings.TrimSpace(raw) == "" {
		return b
	}
	_ = json.Unmarshal([]byte(raw), &b)
	return b
}
