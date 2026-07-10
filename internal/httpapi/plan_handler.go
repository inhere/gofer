package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

type planView struct {
	PlanID      string `json:"plan_id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Owner       string `json:"owner,omitempty"`
	Progress    int    `json:"progress,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func toPlanView(p jobstore.Plan) planView {
	return planView{
		PlanID: p.PlanID, Title: p.Title, Description: p.Description,
		Status: p.Status, Owner: p.Owner, Progress: p.Progress,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type createPlanReq struct {
	PlanID      string `json:"plan_id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

func (s *Server) handleCreatePlan(c *rux.Context) {
	var body createPlanReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	planID := strings.TrimSpace(body.PlanID)
	if planID == "" {
		planID = "plan-" + time.Now().Format(job.JobIDLayout) + "-" + job.RandomSuffix()
	}
	now := time.Now().Unix()
	p := jobstore.Plan{
		PlanID: planID, Title: body.Title, Description: body.Description,
		Status: jobstore.PlanOpen, Owner: callerFromCtx(c),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.jobs.Meta().InsertPlan(p); err != nil {
		writeError(c, http.StatusInternalServerError, "create plan failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toPlanView(p))
}

func (s *Server) handleListPlans(c *rux.Context) {
	list, err := s.jobs.Meta().ListPlans(c.Query("status"), 0)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plans failed", err.Error())
		return
	}
	out := make([]planView, 0, len(list))
	for _, p := range list {
		out = append(out, toPlanView(p))
	}
	c.JSON(http.StatusOK, map[string]any{"plans": out})
}

type planDetail struct {
	planView
	Jobs []job.JobResult `json:"jobs"`
}

func (s *Server) handleGetPlan(c *rux.Context) {
	id := c.Param("id")
	p, ok, err := s.jobs.Meta().GetPlan(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	jobs, err := s.jobs.ListJobs(job.ListOpts{Plan: id, Limit: 1000})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plan jobs failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, planDetail{planView: toPlanView(p), Jobs: jobs})
}

type attachJobReq struct {
	JobID string `json:"job_id"`
}

func (s *Server) handleAttachPlanJob(c *rux.Context) {
	id := c.Param("id")
	var body attachJobReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	jobID := strings.TrimSpace(body.JobID)
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "job_id required", "attach requires a job_id")
		return
	}
	if _, ok, err := s.jobs.Meta().GetPlan(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	ok, err := s.jobs.Meta().AttachJobToPlan(jobID, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "attach job failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+jobID)
		return
	}
	_ = s.jobs.Meta().TouchPlan(id)
	p, _, _ := s.jobs.Meta().GetPlan(id)
	c.JSON(http.StatusOK, toPlanView(p))
}
