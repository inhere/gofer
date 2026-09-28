package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

type planHandoffPutReq struct {
	Body            string `json:"body"`
	ExpectedVersion int    `json:"expected_version"`
}

func (s *Server) planHandoffPlan(c *rux.Context) (jobstore.Plan, bool) {
	planID := c.Param("id")
	p, ok, err := s.jobs.Meta().GetPlan(planID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return jobstore.Plan{}, false
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+planID)
		return jobstore.Plan{}, false
	}
	return p, true
}

func (s *Server) handleGetPlanHandoff(c *rux.Context) {
	if _, ok := s.planHandoffPlan(c); !ok {
		return
	}
	version := 0
	if raw := strings.TrimSpace(c.Req.URL.Query().Get("version")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(c, http.StatusBadRequest, "invalid version", "version must be a positive integer")
			return
		}
		version = parsed
	}
	h, ok, err := s.jobs.Meta().GetPlanHandoff(c.Param("id"), version)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get plan handoff failed", err.Error())
		return
	}
	if !ok {
		c.JSON(http.StatusOK, nil)
		return
	}
	c.JSON(http.StatusOK, h)
}

func (s *Server) handleListPlanHandoffHistory(c *rux.Context) {
	if _, ok := s.planHandoffPlan(c); !ok {
		return
	}
	history, err := s.jobs.Meta().ListPlanHandoffHistory(c.Param("id"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plan handoff history failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, history)
}

func (s *Server) handlePutPlanHandoff(c *rux.Context) {
	plan, ok := s.planHandoffPlan(c)
	if !ok {
		return
	}
	if jc, isJob := jobCallerFromCtx(c); isJob && jc.PlanID != plan.PlanID {
		writeError(c, http.StatusForbidden, "job credential may not write another plan's handoff", "the credential is attached to plan "+jc.PlanID)
		return
	}
	var body planHandoffPutReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.ExpectedVersion < 0 {
		writeError(c, http.StatusBadRequest, "invalid expected_version", "expected_version must be zero or positive")
		return
	}
	h, err := s.jobs.Meta().SetPlanHandoff(plan.PlanID, body.Body, callerFromCtx(c), body.ExpectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, jobstore.ErrPlanHandoffTooLarge):
			writeError(c, http.StatusBadRequest, "handoff too large", err.Error())
		case errors.Is(err, jobstore.ErrPlanHandoffConflict):
			writeError(c, http.StatusConflict, "handoff changed", "the plan handoff was updated; read the latest version and retry")
		default:
			writeError(c, http.StatusInternalServerError, "set plan handoff failed", err.Error())
		}
		return
	}
	s.jobs.RecordScopedEvent(job.PlanEventScope(plan.PlanID), job.EventPlanHandoffUpdated, plan.ProjectKey, map[string]any{
		"plan_id": h.PlanID, "version": h.Version, "by": h.By,
	})
	c.JSON(http.StatusOK, h)
}
