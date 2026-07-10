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

// planListItem 是 list 响应项：header + 其下 jobs 的 counts（列表进度条数据源，P4/T10）。
// detail（planDetail）另含 jobs/todos；list 仅带 counts 保持轻量。
type planListItem struct {
	planView
	Counts jobstore.PlanCounts `json:"counts"`
}

func toPlanView(p jobstore.Plan) planView {
	return planView{
		PlanID: p.PlanID, Title: p.Title, Description: p.Description,
		Status: p.Status, Owner: p.Owner, Progress: p.Progress,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type todoView struct {
	TodoID    string `json:"todo_id"`
	PlanID    string `json:"plan_id"`
	JobID     string `json:"job_id,omitempty"`
	Title     string `json:"title"`
	Done      bool   `json:"done"`
	Sort      int    `json:"sort,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func toTodoView(t jobstore.PlanTodo) todoView {
	return todoView{
		TodoID: t.TodoID, PlanID: t.PlanID, JobID: t.JobID, Title: t.Title,
		Done: t.Done, Sort: t.Sort, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
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
	out := make([]planListItem, 0, len(list))
	for _, p := range list {
		raw, cErr := s.jobs.Meta().PlanJobStatusCounts(p.PlanID)
		if cErr != nil {
			writeError(c, http.StatusInternalServerError, "plan counts failed", cErr.Error())
			return
		}
		out = append(out, planListItem{
			planView: toPlanView(p),
			Counts:   jobstore.RollupPlanCounts(raw),
		})
	}
	c.JSON(http.StatusOK, map[string]any{"plans": out})
}

type planDetail struct {
	planView
	Counts jobstore.PlanCounts `json:"counts"`
	Jobs   []job.JobResult     `json:"jobs"`
	Todos  []todoView          `json:"todos"`
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
	raw, err := s.jobs.Meta().PlanJobStatusCounts(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "plan counts failed", err.Error())
		return
	}
	todos, err := s.jobs.Meta().ListTodosByPlan(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plan todos failed", err.Error())
		return
	}
	todoViews := make([]todoView, 0, len(todos))
	for _, t := range todos {
		todoViews = append(todoViews, toTodoView(t))
	}
	c.JSON(http.StatusOK, planDetail{
		planView: toPlanView(p),
		Counts:   jobstore.RollupPlanCounts(raw),
		Jobs:     jobs,
		Todos:    todoViews,
	})
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

type addTodoReq struct {
	Title string `json:"title"`
	JobID string `json:"job_id,omitempty"`
	Sort  int    `json:"sort,omitempty"`
}

func (s *Server) handleAddPlanTodo(c *rux.Context) {
	id := c.Param("id")
	var body addTodoReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(c, http.StatusBadRequest, "title required", "a todo requires a title")
		return
	}
	if _, ok, err := s.jobs.Meta().GetPlan(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	now := time.Now()
	t := jobstore.PlanTodo{
		TodoID:    "todo-" + now.Format(job.JobIDLayout) + "-" + job.RandomSuffix(),
		PlanID:    id,
		JobID:     strings.TrimSpace(body.JobID),
		Title:     body.Title,
		Sort:      body.Sort,
		CreatedAt: now.Unix(),
		UpdatedAt: now.Unix(),
	}
	if err := s.jobs.Meta().InsertTodo(t); err != nil {
		writeError(c, http.StatusInternalServerError, "add todo failed", err.Error())
		return
	}
	_ = s.jobs.Meta().TouchPlan(id)
	c.JSON(http.StatusOK, toTodoView(t))
}

type updateTodoReq struct {
	Done bool `json:"done"`
}

func (s *Server) handleUpdateTodo(c *rux.Context) {
	tid := c.Param("todo_id")
	var body updateTodoReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	ok, err := s.jobs.Meta().SetTodoDone(tid, body.Done)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "update todo failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown todo", "no todo with id "+tid)
		return
	}
	t, _, err := s.jobs.Meta().GetTodo(tid)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get todo failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toTodoView(t))
}
