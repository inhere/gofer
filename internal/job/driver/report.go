package driver

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// PlanDriverReport implements job.PlanDriver (plan R7): the plan's recorded actions
// against what actually happened. It first settles every action it can (the same
// observation a plan event runs), so the report is current even for a plan nothing is
// advancing any more, then totals by action and by rule.
func (e *Engine) PlanDriverReport(_ context.Context, planID string) (job.PlanDriverReport, error) {
	meta := e.ops.Meta()
	now := e.ops.Now()
	e.observe(planID, now)
	rep := job.PlanDriverReport{
		PlanID: planID, WindowHours: int(ObserveWindow.Hours()),
		ByAction: []job.PlanDriverReportRow{}, ByRule: []job.PlanDriverReportRow{},
	}
	drv, ok, err := meta.GetPlanDriver(planID)
	if err != nil {
		return rep, err
	}
	if ok {
		rep.Enabled, rep.Level, rep.Budget = true, drv.Level, parseBudget(drv.BudgetJSON)
		if spent, err := e.spend(drv, now); err == nil {
			rep.Spent = spent
		}
	}
	actions, err := meta.ListPlanDriverActions(planID, false)
	if err != nil {
		return rep, err
	}
	rep.Total = len(actions)
	if len(actions) > 0 {
		rep.ByAction, rep.ByRule = summarize(actions)
	}
	for _, a := range actions {
		if a.Outcome == "" {
			rep.Pending++
		}
	}
	rep.Recent = recentActions(actions, job.PlanDriverReportRecent)
	return rep, nil
}

type ruleKey struct{ action, rule string }

// summarize totals actions (oldest first) by action and by action+rule.
func summarize(actions []jobstore.PlanDriverAction) (byAction, byRule []job.PlanDriverReportRow) {
	acts := map[string]*job.PlanDriverReportRow{}
	rules := map[ruleKey]*job.PlanDriverReportRow{}
	resolved := map[string][]string{} // action -> outcomes, oldest first
	add := func(r *job.PlanDriverReportRow, outcome string) {
		r.Total++
		switch outcome {
		case OutcomeMatch:
			r.Match++
		case OutcomeMismatch:
			r.Mismatch++
		case OutcomeNoFollowup:
			r.NoFollowup++
		default:
			r.Pending++
		}
	}
	for _, a := range actions {
		ar := acts[a.Action]
		if ar == nil {
			ar = &job.PlanDriverReportRow{Action: a.Action}
			acts[a.Action] = ar
		}
		k := ruleKey{a.Action, a.Rule}
		rr := rules[k]
		if rr == nil {
			rr = &job.PlanDriverReportRow{Action: a.Action, Rule: a.Rule}
			rules[k] = rr
		}
		add(ar, a.Outcome)
		add(rr, a.Outcome)
		if a.Outcome != "" {
			resolved[a.Action] = append(resolved[a.Action], a.Outcome)
		}
	}
	for _, r := range acts {
		r.Rate = rate(*r)
		recent := resolved[r.Action]
		if len(recent) > job.PlanDriverUpgradeWindow {
			recent = recent[len(recent)-job.PlanDriverUpgradeWindow:]
		}
		for _, o := range recent {
			if o == OutcomeMatch {
				r.RecentMatch++
			}
		}
		r.RecentN = len(recent)
		if r.RecentN > 0 {
			r.RecentRate = float64(r.RecentMatch) / float64(r.RecentN)
		}
		r.UpgradeSuggested = r.RecentN == job.PlanDriverUpgradeWindow && r.RecentRate >= job.PlanDriverUpgradeRate
		byAction = append(byAction, *r)
	}
	for _, r := range rules {
		r.Rate = rate(*r)
		byRule = append(byRule, *r)
	}
	sort.Slice(byAction, func(i, j int) bool { return byAction[i].Action < byAction[j].Action })
	sort.Slice(byRule, func(i, j int) bool {
		if byRule[i].Action != byRule[j].Action {
			return byRule[i].Action < byRule[j].Action
		}
		return byRule[i].Rule < byRule[j].Rule
	})
	return byAction, byRule
}

func rate(r job.PlanDriverReportRow) float64 {
	n := r.Match + r.Mismatch + r.NoFollowup
	if n == 0 {
		return 0
	}
	return float64(r.Match) / float64(n)
}

// recentActions lists the newest n actions (the store returns them oldest first),
// newest first, for the report's line-by-line view.
func recentActions(actions []jobstore.PlanDriverAction, n int) []job.PlanDriverActionView {
	out := make([]job.PlanDriverActionView, 0, min(len(actions), n))
	for i := len(actions) - 1; i >= 0 && len(out) < n; i-- {
		a := actions[i]
		v := job.PlanDriverActionView{
			ID: a.ID, TodoID: a.TodoID, JobID: a.JobID, EventKind: a.EventKind,
			Action: a.Action, Rule: a.Rule, Risk: a.Risk, WouldPause: a.WouldPause,
			Outcome: a.Outcome, MatchedAt: a.MatchedAt, CreatedAt: a.CreatedAt,
		}
		if json.Valid([]byte(a.ParamsJSON)) {
			v.Params = json.RawMessage(a.ParamsJSON)
		}
		out = append(out, v)
	}
	return out
}
