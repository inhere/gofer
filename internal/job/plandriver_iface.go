package job

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/inhere/gofer/internal/jobstore"
)

// plandriver_iface.go is the job -> driver seam (G024, the same shape as the workflow
// seam): the job package defines PlanDriver and never imports internal/job/driver; the
// driver Engine implements it and core injects it via SetPlanDriver. The driver reaches the
// host through its own HostOps interface (it imports job, not the other way round).
//
// The driver is OBSERVING at the record level (plan R3 / R6): it is called after a plan
// event was processed, only for plans whose driver is on, and it must never change what
// the advance did. Nothing it returns or panics with reaches the progression: every call
// goes through the guards below, which only log.

// PlanEventView is the part of a processed plan event the driver is given.
type PlanEventView struct {
	PlanID string
	// TodoID / JobID name the todo and job the event is about ("" = a plan-level event:
	// a human's run / resume, a sweep).
	TodoID string
	JobID  string
	// Kind is PlanEventAdvance / PlanEventBlock / PlanEventSweep.
	Kind string
	// By is the caller identity the advance acted for.
	By string
}

// PlanDriver is what the plan event handler calls around one event.
type PlanDriver interface {
	// BeforePlanEvent runs BEFORE the event is processed and returns whatever the driver
	// needs to remember about the state it is about to be changed; nil means "this plan's
	// driver is off, do not call OnPlanEvent".
	BeforePlanEvent(ctx context.Context, ev PlanEventView) any
	// OnPlanEvent runs AFTER the event was processed successfully, in the plan's serialised
	// handler, with the value BeforePlanEvent returned.
	OnPlanEvent(ctx context.Context, ev PlanEventView, before any)
	// PlanDriverReport answers `plan drive report`: the recorded actions against what
	// actually happened, per action and per rule.
	PlanDriverReport(ctx context.Context, planID string) (PlanDriverReport, error)
}

// SetPlanDriver injects the plan driver; called once at assemble time (internal/core).
// nil (or never calling it) leaves the driver unwired: events are processed as before.
func (s *Service) SetPlanDriver(d PlanDriver) { s.planDriver = d }

// ErrPlanDriverUnavailable is returned by PlanDriverReport on a service no driver was wired into.
var ErrPlanDriverUnavailable = errors.New("plan driver is not available")

// Thresholds of the suggested-upgrade condition (design Q5): the most recent
// PlanDriverUpgradeWindow resolved actions of one kind agree at least PlanDriverUpgradeRate
// of the time. The report only shows it; nothing is upgraded automatically.
const (
	PlanDriverUpgradeWindow = 20
	PlanDriverUpgradeRate   = 0.9
)

// PlanDriverReportRow is one line of the report: an action (ByAction) or an action's rule
// (ByRule), with what its outcomes came to.
type PlanDriverReportRow struct {
	Action string `json:"action"`
	// Rule is set on the ByRule rows only.
	Rule  string `json:"rule,omitempty"`
	Total int    `json:"total"`
	// Match / Mismatch / NoFollowup are the resolved outcomes; Pending is not observed yet.
	Match      int `json:"match"`
	Mismatch   int `json:"mismatch"`
	NoFollowup int `json:"no_followup"`
	Pending    int `json:"pending"`
	// Rate is match / (match + mismatch + no_followup); a no_followup is not agreement.
	// 0 with no resolved action.
	Rate float64 `json:"rate"`
	// RecentN / RecentMatch / RecentRate cover the latest PlanDriverUpgradeWindow resolved
	// actions (ByAction rows only); UpgradeSuggested is the "20 in a row at >= 90%" verdict.
	RecentN          int     `json:"recent_n,omitempty"`
	RecentMatch      int     `json:"recent_match,omitempty"`
	RecentRate       float64 `json:"recent_rate,omitempty"`
	UpgradeSuggested bool    `json:"upgrade_suggested,omitempty"`
}

// PlanDriverSpent is the driver's accounting against the plan's budget.
type PlanDriverSpent struct {
	Jobs   int     `json:"jobs"`
	Rework int     `json:"rework"`
	Hours  float64 `json:"hours"`
}

// PlanDriverReport is the answer of `gofer plan drive report` and GET
// /v1/plans/{id}/driver/report. Field names are part of the HTTP contract.
type PlanDriverReport struct {
	PlanID  string           `json:"plan_id"`
	Enabled bool             `json:"enabled"`
	Level   string           `json:"level,omitempty"`
	Budget  PlanDriverBudget `json:"budget"`
	Spent   PlanDriverSpent  `json:"spent"`
	// Total counts every recorded action; Pending those not observed yet.
	Total   int `json:"total"`
	Pending int `json:"pending"`
	// WindowHours is how long an action is observed before it reads no_followup.
	WindowHours int                   `json:"window_hours"`
	ByAction    []PlanDriverReportRow `json:"by_action"`
	ByRule      []PlanDriverReportRow `json:"by_rule"`
	// Recent is the newest recorded actions (at most PlanDriverReportRecent), newest
	// first: what the driver would have done, so a person can check it line by line.
	Recent []PlanDriverActionView `json:"recent"`
}

// PlanDriverReportRecent caps PlanDriverReport.Recent.
const PlanDriverReportRecent = 100

// PlanDriverActionView is one recorded action as the report shows it.
type PlanDriverActionView struct {
	ID         int64           `json:"id"`
	TodoID     string          `json:"todo_id,omitempty"`
	JobID      string          `json:"job_id,omitempty"`
	EventKind  string          `json:"event_kind"`
	Action     string          `json:"action"`
	Rule       string          `json:"rule"`
	Risk       string          `json:"risk"`
	WouldPause bool            `json:"would_pause,omitempty"`
	Outcome    string          `json:"outcome,omitempty"`
	MatchedAt  int64           `json:"matched_at,omitempty"`
	CreatedAt  int64           `json:"created_at"`
	Params     json.RawMessage `json:"params,omitempty"`
}

// PlanDriverReport asks the wired driver for a plan's report. An unknown plan is
// ErrPlanNotFound.
func (s *Service) PlanDriverReport(ctx context.Context, planID string) (PlanDriverReport, error) {
	if _, ok, err := s.meta.GetPlan(planID); err != nil {
		return PlanDriverReport{}, err
	} else if !ok {
		return PlanDriverReport{}, errors.Join(ErrPlanNotFound, errors.New("unknown plan "+planID))
	}
	if s.planDriver == nil {
		return PlanDriverReport{}, ErrPlanDriverUnavailable
	}
	return s.planDriver.PlanDriverReport(ctx, planID)
}

// planDriverBefore is the guarded BeforePlanEvent: a driver that panics or is not wired
// reads as "off" for this event.
func (s *Service) planDriverBefore(q queuedPlanEvent) (ticket any) {
	d := s.planDriver
	if d == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("plan driver: before event panicked", "plan_id", q.ev.PlanID, "panic", r)
			ticket = nil
		}
	}()
	return d.BeforePlanEvent(context.Background(), planEventView(q.ev))
}

// planDriverAfter is the guarded OnPlanEvent. The driver's failures stop here: the advance
// it observed already happened and must not be touched.
func (s *Service) planDriverAfter(q queuedPlanEvent, ticket any) {
	d := s.planDriver
	if d == nil || ticket == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("plan driver: event panicked", "plan_id", q.ev.PlanID, "panic", r)
		}
	}()
	d.OnPlanEvent(context.Background(), planEventView(q.ev), ticket)
}

func planEventView(ev PlanEvent) PlanEventView {
	return PlanEventView{PlanID: ev.PlanID, TodoID: ev.TodoID, JobID: ev.JobID, Kind: ev.Kind, By: ev.By}
}

// advancePlanFor is advancePlan for an advance a finished job caused: the event carries the
// todo and the job, which is how the driver knows what the advance was about. The advance
// itself is identical.
func (s *Service) advancePlanFor(planID, todoID, jobID, by string) {
	s.emitPlanEvent(PlanEvent{PlanID: planID, TodoID: todoID, JobID: jobID, Kind: PlanEventAdvance, By: by}, nil)
}

// TerminationReasonOf is TerminationReason for a stored job row, for readers that hold the
// record and not a service (the driver's rules).
func TerminationReasonOf(rec jobstore.JobRecord) string { return TerminationReason(fromRecord(rec)) }

// observePlanJob delivers a PlanEventObserve for a job outcome the advance does not act on.
func (s *Service) observePlanJob(planID, todoID, jobID, by string) {
	if s.planDriver == nil {
		return
	}
	s.emitPlanEvent(PlanEvent{PlanID: planID, TodoID: todoID, JobID: jobID, Kind: PlanEventObserve, By: by}, nil)
}
