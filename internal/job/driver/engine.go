package driver

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Engine is the plan driver. It implements job.PlanDriver.
type Engine struct {
	ops HostOps
}

var _ job.PlanDriver = (*Engine)(nil)

// NewEngine builds a driver over the host (core passes the job service).
func NewEngine(ops HostOps) *Engine { return &Engine{ops: ops} }

// ticket is what BeforePlanEvent remembers about the state the host is about to change.
type ticket struct {
	// at is when the event started being processed; it stamps the actions it produces.
	at time.Time
	// dispatch lists the todos the chain would have started at this event.
	dispatch []string
}

// BeforePlanEvent implements job.PlanDriver: a plan whose driver is off returns nil and is
// never observed; otherwise it captures what the advance is about to move.
func (e *Engine) BeforePlanEvent(_ context.Context, ev job.PlanEventView) any {
	meta := e.ops.Meta()
	drv, ok, err := meta.GetPlanDriver(ev.PlanID)
	if err != nil {
		slog.Warn("plan driver: read state", "plan_id", ev.PlanID, "err", err)
		return nil
	}
	if !ok || drv.Level != job.PlanDriverLevelRecord {
		return nil
	}
	t := &ticket{at: e.ops.Now()}
	if ev.Kind == job.PlanEventBlock {
		return t
	}
	plan, ok, err := meta.GetPlan(ev.PlanID)
	if err != nil || !ok {
		return t
	}
	todos, err := meta.ListTodosByPlan(ev.PlanID)
	if err != nil {
		slog.Warn("plan driver: list todos", "plan_id", ev.PlanID, "err", err)
		return t
	}
	t.dispatch = dispatchCandidates(plan, todos, func(todoID string) bool {
		jobs, err := meta.ListJobsByTodo(todoID, 1)
		return err != nil || len(jobs) > 0
	})
	return t
}

// OnPlanEvent implements job.PlanDriver: it records the actions the rules pick for the
// processed event, keeps the plan's accounting, and observes what actually became of the
// actions recorded earlier. It only ever writes the driver's own tables.
func (e *Engine) OnPlanEvent(_ context.Context, ev job.PlanEventView, before any) {
	t, _ := before.(*ticket)
	if t == nil {
		return
	}
	meta := e.ops.Meta()
	drv, ok, err := meta.GetPlanDriver(ev.PlanID)
	if err != nil || !ok || drv.Level != job.PlanDriverLevelRecord {
		return
	}
	now := e.ops.Now()
	budget := parseBudget(drv.BudgetJSON)
	spent, err := e.spend(drv, now)
	if err != nil {
		slog.Warn("plan driver: spend", "plan_id", ev.PlanID, "err", err)
		return
	}
	var decisions []Decision
	if ev.JobID != "" {
		if d, ok := e.decideForJob(ev, reworkBudget{Limit: budget.MaxRework, Spent: spent.Rework}); ok {
			decisions = append(decisions, d)
		}
	}
	for _, todoID := range t.dispatch {
		decisions = append(decisions, Decision{
			Action: ActionDispatchTodo, Rule: RuleDispatchReady, TodoID: todoID, Params: map[string]any{},
		})
	}
	for _, d := range decisions {
		e.record(ev, d, t.at, false)
	}
	// A recorded resume_with_notes counts as a rework round, so re-read the spend.
	if spent, err = e.spend(drv, now); err != nil {
		slog.Warn("plan driver: spend", "plan_id", ev.PlanID, "err", err)
		return
	}
	e.account(ev, drv, budget, spent, t.at)
	e.observe(ev.PlanID, now)
}

// decideForJob loads the facts of the event's job and applies the rules.
func (e *Engine) decideForJob(ev job.PlanEventView, rw reworkBudget) (Decision, bool) {
	rec, ok, err := e.ops.Meta().GetJob(ev.JobID)
	if err != nil || !ok {
		if err != nil {
			slog.Warn("plan driver: read job", "plan_id", ev.PlanID, "job_id", ev.JobID, "err", err)
		}
		return Decision{}, false
	}
	f := jobFacts{
		Status:       rec.Status,
		Termination:  job.TerminationReasonOf(rec),
		SelfDecision: rec.ResultDecision,
		SelfReason:   rec.ResultReason,
		CheckVerdict: checkVerdict(rec.CheckJSON),
	}
	d, ok := decideJob(ev.Kind, f, rw)
	if !ok {
		return Decision{}, false
	}
	d.TodoID, d.JobID = ev.TodoID, ev.JobID
	return d, true
}

// checkVerdict reads the delivery check's verdict out of its stored document; anything
// unreadable or without a pass / fail verdict reads as "no check".
func checkVerdict(raw string) string {
	if raw == "" {
		return ""
	}
	var c job.CheckResult
	if json.Unmarshal([]byte(raw), &c) != nil {
		return ""
	}
	if c.Verdict == "pass" || c.Verdict == "fail" {
		return c.Verdict
	}
	return ""
}

// record stores one decision as an action, unless the same one is already on file (a
// replayed event must not log twice): a job-bound action is unique per (todo, job, action,
// rule), a plan-level one only while the earlier copy is still unobserved.
func (e *Engine) record(ev job.PlanEventView, d Decision, at time.Time, wouldPause bool) {
	meta := e.ops.Meta()
	existing, err := meta.ListPlanDriverActions(ev.PlanID, false)
	if err != nil {
		slog.Warn("plan driver: list actions", "plan_id", ev.PlanID, "err", err)
		return
	}
	for _, a := range existing {
		if d.Action == ActionPauseBudget {
			break // account() already logs one per distinct budget state
		}
		if a.TodoID != d.TodoID || a.JobID != d.JobID || a.Action != d.Action || a.Rule != d.Rule {
			continue
		}
		if d.JobID != "" || a.Outcome == "" {
			return
		}
	}
	params := "{}"
	if len(d.Params) > 0 {
		if raw, err := json.Marshal(d.Params); err == nil {
			params = string(raw)
		}
	}
	if _, err := meta.InsertPlanDriverAction(jobstore.PlanDriverAction{
		PlanID: ev.PlanID, TodoID: d.TodoID, JobID: d.JobID, EventKind: ev.Kind,
		Action: d.Action, ParamsJSON: params, Risk: RiskOf(d.Action), Rule: d.Rule,
		WouldPause: wouldPause, CreatedAt: at.Unix(),
	}); err != nil {
		slog.Warn("plan driver: record action", "plan_id", ev.PlanID, "action", d.Action, "err", err)
	}
}
