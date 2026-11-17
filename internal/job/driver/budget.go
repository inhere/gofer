package driver

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// budget.go is the unified per-plan budget (plan R8): what the plan has spent since the
// driver was enabled, written to plan_drivers.spent_json, and the `pause_budget` action
// that says "this would have paused the plan". At the record level nothing is paused.

func parseBudget(raw string) job.PlanDriverBudget {
	var b job.PlanDriverBudget
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &b)
	}
	return b
}

// spend counts what the plan has used since its driver was enabled: the jobs its items
// started (continuations and retries included), the rework rounds (the resume_with_notes
// the driver recorded plus the delivery check's continuations) and the hours.
func (e *Engine) spend(drv jobstore.PlanDriver, now time.Time) (job.PlanDriverSpent, error) {
	meta := e.ops.Meta()
	js, err := meta.PlanJobSpendSince(drv.PlanID, drv.EnabledAt)
	if err != nil {
		return job.PlanDriverSpent{}, err
	}
	actions, err := meta.ListPlanDriverActions(drv.PlanID, false)
	if err != nil {
		return job.PlanDriverSpent{}, err
	}
	rework := js.CheckResumes
	for _, a := range actions {
		if a.Action == ActionResumeWithNotes {
			rework++
		}
	}
	hours := 0.0
	if d := now.Unix() - drv.EnabledAt; d > 0 {
		hours = math.Round(float64(d)/36) / 100 // two decimals
	}
	return job.PlanDriverSpent{Jobs: js.Jobs, Rework: rework, Hours: hours}, nil
}

// exceeded names the budget dimensions that are used up (a ceiling of 0 is unlimited).
func exceeded(b job.PlanDriverBudget, s job.PlanDriverSpent) []string {
	var out []string
	if b.MaxJobs > 0 && s.Jobs >= b.MaxJobs {
		out = append(out, "max_jobs")
	}
	if b.MaxRework > 0 && s.Rework >= b.MaxRework {
		out = append(out, "max_rework")
	}
	if b.MaxHours > 0 && s.Hours >= b.MaxHours {
		out = append(out, "max_hours")
	}
	return out
}

// account writes the spend and, when a ceiling is used up, records a would-pause action —
// once per distinct state (the same dimensions at the same job / rework counts), so a plan
// that stays over budget does not log one per event.
func (e *Engine) account(ev job.PlanEventView, drv jobstore.PlanDriver, b job.PlanDriverBudget, s job.PlanDriverSpent, at time.Time) {
	meta := e.ops.Meta()
	if raw, err := json.Marshal(s); err == nil {
		if _, err := meta.UpdatePlanDriverSpent(ev.PlanID, string(raw)); err != nil {
			slog.Warn("plan driver: write spend", "plan_id", ev.PlanID, "err", err)
		}
	}
	over := exceeded(b, s)
	if len(over) == 0 {
		return
	}
	sort.Strings(over)
	sig := fmt.Sprintf("%s|jobs=%d|rework=%d", strings.Join(over, ","), s.Jobs, s.Rework)
	actions, err := meta.ListPlanDriverActions(ev.PlanID, false)
	if err != nil {
		slog.Warn("plan driver: list actions", "plan_id", ev.PlanID, "err", err)
		return
	}
	for i := len(actions) - 1; i >= 0; i-- {
		if actions[i].Action != ActionPauseBudget {
			continue
		}
		var prev struct {
			Sig string `json:"sig"`
		}
		_ = json.Unmarshal([]byte(actions[i].ParamsJSON), &prev)
		if prev.Sig == sig {
			return
		}
		break
	}
	e.record(ev, Decision{
		Action: ActionPauseBudget, Rule: RuleBudgetExhausted,
		Params: map[string]any{"sig": sig, "exceeded": over, "spent": s, "budget": b},
	}, at, true)
}
