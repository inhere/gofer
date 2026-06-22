package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// workflowDetail is the GET /v1/workflows/{id} response: the workflow header plus
// its per-step summary (the step chain). It keeps the header fields snake_case
// and inlines the steps so the web/CLI can render the chain in one fetch.
type workflowDetail struct {
	ID          string             `json:"id"`
	Title       string             `json:"title,omitempty"`
	Status      string             `json:"status"`
	CurrentStep int                `json:"current_step"`
	TotalSteps  int                `json:"total_steps"`
	CallerID    string             `json:"caller_id,omitempty"`
	Error       string             `json:"error,omitempty"`
	CreatedAt   int64              `json:"created_at"`
	UpdatedAt   int64              `json:"updated_at"`
	Steps       []job.WorkflowStep `json:"steps"`
}

// handleCreateWorkflow parses a WorkflowSpec, submits it and returns the created
// workflow header (running, step 1 started). The authenticated caller id is
// stamped server-side (anti-spoof, like handleCreateJob). Validation failures map
// to 404 (unknown project) or 400 (anything else) via the same submitStatus
// sentinels the single-job path uses.
func (s *Server) handleCreateWorkflow(c *rux.Context) {
	var spec job.WorkflowSpec
	if err := c.BindJSON(&spec); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	callerID := callerFromCtx(c)
	wf, err := s.jobs.SubmitWorkflow(spec, callerID)
	if err != nil {
		writeError(c, submitStatus(err), "workflow rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, toWorkflowSummary(wf))
}

// handleGetWorkflow returns a workflow header + its step chain; an unknown id is
// a 404.
func (s *Server) handleGetWorkflow(c *rux.Context) {
	id := c.Param("id")
	wf, ok, err := s.jobs.GetWorkflow(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get workflow failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown workflow", "no workflow with id "+id)
		return
	}
	steps, err := s.jobs.WorkflowSteps(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list workflow steps failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, workflowDetail{
		ID: wf.ID, Title: wf.Title, Status: wf.Status,
		CurrentStep: wf.CurrentStep, TotalSteps: wf.TotalSteps,
		CallerID: wf.CallerID, Error: wf.Error,
		CreatedAt: wf.CreatedAt, UpdatedAt: wf.UpdatedAt,
		Steps: steps,
	})
}

// handleListWorkflows returns workflow headers, optionally filtered by ?status=.
// The list is always a non-nil array, so an empty result serialises as
// {"workflows":[]}.
func (s *Server) handleListWorkflows(c *rux.Context) {
	list, err := s.jobs.ListWorkflows(c.Query("status"), 0)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list workflows failed", err.Error())
		return
	}
	out := make([]workflowSummary, 0, len(list))
	for _, wf := range list {
		out = append(out, toWorkflowSummary(wf))
	}
	c.JSON(http.StatusOK, map[string]any{"workflows": out})
}

// handleListWorkflowEvents returns a workflow's append-only lifecycle events (P1)
// in seq order. An unknown workflow id is a 404 (consistent with handleGetWorkflow).
// The optional ?since=<seq> returns only events strictly after that cursor
// (incremental poll). The list is always a non-nil array, so an empty result
// serialises as {"events":[]}.
func (s *Server) handleListWorkflowEvents(c *rux.Context) {
	id := c.Param("id")
	if _, ok, err := s.jobs.GetWorkflow(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get workflow failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown workflow", "no workflow with id "+id)
		return
	}
	// since 非数值 -> 0 -> 不过滤（仿 job events 的容错）。
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	events, err := s.jobs.ListWorkflowEvents(id, since)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list workflow events failed", err.Error())
		return
	}
	if events == nil {
		events = []jobstore.WorkflowEvent{}
	}
	c.JSON(http.StatusOK, map[string]any{"events": events})
}

// handleCancelWorkflow cancels a running workflow (marks cancelled + cancels the
// current step's job). It is a stable no-op for an already-terminal workflow and
// a 404 for an unknown id. Returns the updated header snapshot.
func (s *Server) handleCancelWorkflow(c *rux.Context) {
	id := c.Param("id")
	if err := s.jobs.CancelWorkflow(id); err != nil {
		// The only CancelWorkflow error is an unknown id (terminal is a no-op).
		writeError(c, http.StatusNotFound, "unknown workflow", err.Error())
		return
	}
	wf, _, _ := s.jobs.GetWorkflow(id)
	c.JSON(http.StatusOK, toWorkflowSummary(wf))
}

// workflowSummary is the list/create/cancel response shape (header only, no step
// list — the detail endpoint carries the chain).
type workflowSummary struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Status      string `json:"status"`
	CurrentStep int    `json:"current_step"`
	TotalSteps  int    `json:"total_steps"`
	CallerID    string `json:"caller_id,omitempty"`
	Error       string `json:"error,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// toWorkflowSummary projects a jobstore.Workflow onto the API summary shape.
func toWorkflowSummary(wf jobstore.Workflow) workflowSummary {
	return workflowSummary{
		ID: wf.ID, Title: wf.Title, Status: wf.Status,
		CurrentStep: wf.CurrentStep, TotalSteps: wf.TotalSteps,
		CallerID: wf.CallerID, Error: wf.Error,
		CreatedAt: wf.CreatedAt, UpdatedAt: wf.UpdatedAt,
	}
}
