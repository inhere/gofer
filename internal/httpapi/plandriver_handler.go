package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
)

// The plan driver's three entries (design
// docs/design/2026-11-16-plan-driver-autonomy-design.md §1, plan R4): turn a plan's
// autonomy on at a level with a budget (PUT), turn it off (DELETE), and read its state
// (GET). They are the switches only — what the driver decides and does lives in the job
// layer (internal/job/plandriver_state.go and, for the rules, the driver package).
//
// The handlers only bind, authorise and forward (G021). The write entries belong to the
// plan's owner or a holder of plan.manage.others (mayManagePlanOf, the same rule as
// pause / attach); the read is Public like GET /v1/plans/{id}, so a plan page can show
// the switch without asking for a permission it does not need.

// planDriverReq is the PUT body: the level ("" = the default `record`) and the budget.
// An omitted budget means "no ceiling" — the zero value of every field is unlimited.
type planDriverReq struct {
	Level  string                `json:"level,omitempty"`
	Budget *job.PlanDriverBudget `json:"budget,omitempty"`
}

// handleSetPlanDriver is PUT /v1/plans/{id}/driver: enable (or re-configure) the plan's
// driver and answer the stored state. A level this version does not support, an unknown
// level and a negative budget are 400s from the job layer, before anything is written.
func (s *Server) handleSetPlanDriver(c *rux.Context) {
	id := c.Param("id")
	if !s.mayManagePlanOf(c, id) {
		return
	}
	var body planDriverReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	budget := job.PlanDriverBudget{}
	if body.Budget != nil {
		budget = *body.Budget
	}
	if err := s.jobs.EnablePlanDriver(id, body.Level, budget); err != nil {
		writePlanDriverError(c, err)
		return
	}
	s.writePlanDriverStatus(c, id)
}

// handleDeletePlanDriver is DELETE /v1/plans/{id}/driver: turn the driver off. It is
// idempotent (a plan whose driver is already off answers the same "off" state), because
// the caller asked for a state, not for a deletion.
func (s *Server) handleDeletePlanDriver(c *rux.Context) {
	id := c.Param("id")
	if !s.mayManagePlanOf(c, id) {
		return
	}
	if err := s.jobs.DisablePlanDriver(id); err != nil {
		writePlanDriverError(c, err)
		return
	}
	s.writePlanDriverStatus(c, id)
}

// handleGetPlanDriver is GET /v1/plans/{id}/driver: the plan's driver state (enabled,
// level, budget, the accounting window). An unknown plan is a 404.
func (s *Server) handleGetPlanDriver(c *rux.Context) {
	s.writePlanDriverStatus(c, c.Param("id"))
}

// handleGetPlanDriverReport is GET /v1/plans/{id}/driver/report: the plan's recorded
// driver actions against what actually happened, by action and by rule, with the
// agreement rate and the "ready to upgrade" verdict (plan R7). Reading it settles the
// outcomes that can be settled, which only ever touches the driver's own action log.
func (s *Server) handleGetPlanDriverReport(c *rux.Context) {
	rep, err := s.jobs.PlanDriverReport(c.Req.Context(), c.Param("id"))
	if err != nil {
		writePlanDriverError(c, err)
		return
	}
	c.JSON(http.StatusOK, rep)
}

// writePlanDriverStatus answers with the plan's stored driver state, so every one of the
// three entries replies with the same shape.
func (s *Server) writePlanDriverStatus(c *rux.Context, id string) {
	st, err := s.jobs.PlanDriverStatus(id)
	if err != nil {
		writePlanDriverError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// writePlanDriverError maps the driver errors: an unknown plan is a 404, a request the
// caller must fix (an unsupported or unknown level, a negative budget) is a 400, and
// anything else is the server's.
func writePlanDriverError(c *rux.Context, err error) {
	switch {
	case errors.Is(err, job.ErrPlanNotFound):
		writeError(c, http.StatusNotFound, "unknown plan", err.Error())
	case errors.Is(err, job.ErrPlanDriverUnavailable):
		writeError(c, http.StatusServiceUnavailable, "plan driver unavailable", err.Error())
	case errors.Is(err, job.PlanDriverInvalid):
		writeError(c, http.StatusBadRequest, "invalid plan driver", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "plan driver failed", err.Error())
	}
}
