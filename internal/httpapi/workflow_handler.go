package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
)

type pickWorkflowRequest struct {
	Step int `json:"step"`
	Fan  int `json:"fan"`
}

// workflowDetail is the GET /v1/workflows/{id} response: the workflow header plus
// its per-step summary (the step chain). It keeps the header fields snake_case
// and inlines the steps so the web/CLI can render the chain in one fetch.
type workflowDetail struct {
	ID          string          `json:"id"`
	Title       string          `json:"title,omitempty"`
	Status      string          `json:"status"`
	CurrentStep int             `json:"current_step"`
	TotalSteps  int             `json:"total_steps"`
	CallerID    string          `json:"caller_id,omitempty"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
	RerunOf     string          `json:"rerun_of,omitempty"`
	Steps       []workflow.Step `json:"steps"`
}

// handleCreateWorkflow parses a WorkflowSpec, submits it and returns the created
// workflow header (running, step 1 started). The authenticated caller id is
// stamped server-side (anti-spoof, like handleCreateJob). Validation failures map
// to 404 (unknown project) or 400 (anything else) via the same submitStatus
// sentinels the single-job path uses.
func (s *Server) handleCreateWorkflow(c *rux.Context) {
	var payload map[string]json.RawMessage
	if err := c.BindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	var spec workflow.Spec
	// The body's `project` field is the authoritative project key for this submit;
	// vars.project (the template's own variable) stays supported as the fallback.
	bodyProject := ""
	if raw, ok := payload["project"]; ok {
		if err := json.Unmarshal(raw, &bodyProject); err != nil {
			writeError(c, http.StatusBadRequest, "invalid workflow project", err.Error())
			return
		}
	}
	if raw, ok := payload["template"]; ok {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil || name == "" {
			writeError(c, http.StatusBadRequest, "invalid workflow template", "template must be a non-empty name")
			return
		}
		vars := map[string]string{}
		if rawVars, ok := payload["vars"]; ok {
			if err := json.Unmarshal(rawVars, &vars); err != nil {
				writeError(c, http.StatusBadRequest, "invalid workflow vars", err.Error())
				return
			}
		}
		var err error
		projectKey := bodyProject
		if projectKey == "" {
			projectKey = vars["project"]
		}
		projectDir, globalDir := s.workflowTemplateDirs(projectKey)
		// A template that declares its own `project` variable gets the body's project
		// when the caller did not set the variable explicitly.
		if _, set := vars["project"]; !set && bodyProject != "" {
			for _, tpl := range workflow.LookupWorkflowTemplates(projectDir, globalDir) {
				if tpl.Name == name {
					if _, declared := tpl.Spec.Vars["project"]; declared {
						vars["project"] = bodyProject
					}
					break
				}
			}
		}
		spec, err = workflow.ResolveWorkflowTemplate(name, vars, projectDir, globalDir)
		if err != nil {
			writeError(c, http.StatusBadRequest, "workflow template rejected", err.Error())
			return
		}
		// A template taken from a project's `.gofer/workflows` (or resolving with a
		// project var) runs in that project unless it states its own default.
		if spec.Project == "" {
			spec.Project = projectKey
		}
		if err := workflow.CheckTemplateAgents(s.jobs.Config(), spec); err != nil {
			writeError(c, http.StatusBadRequest, "workflow template rejected", err.Error())
			return
		}
	} else if err := jsonBytes(payload, &spec); err != nil {
		writeError(c, http.StatusBadRequest, "invalid workflow spec", err.Error())
		return
	}
	// BodyProject: every step (and the workflow's default project) must run in a project
	// inside the caller's ceiling.
	if !s.requireProjectScope(c, spec.Project) {
		return
	}
	for _, st := range spec.Steps {
		if !s.requireProjectScope(c, st.ProjectKey) {
			return
		}
	}
	if len(spec.Steps) == 0 && !s.requireProjectScope(c, "") {
		return
	}
	callerID := callerFromCtx(c)
	// repo.push (design §五 trust model): a workflow that pushes branches / tags makes a
	// repository write on the caller's behalf, so the caller must hold the permission on
	// top of the project's delivery.allow_push (checked with the step's other arguments at
	// submit).
	if workflow.SpecUsesPush(spec) && !s.can(c, authz.PermRepoPush) {
		writeError(c, http.StatusForbidden, "workflow rejected",
			"this workflow runs a `uses: gofer/push` step: pushing a branch or tag needs the repo.push permission")
		return
	}
	wf, err := s.workflow.SubmitWorkflow(spec, callerID)
	if err != nil {
		writeError(c, submitStatus(err), "workflow rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, toWorkflowSummary(wf))
}

func jsonBytes(payload map[string]json.RawMessage, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// workflowTemplateDirs resolves the project (?project=<key>) and global template
// directories used by list / show / source / run. The project directory is the workspace as
// the gofer process sees it (Config.ExecPath, G002: host_path, or container_path when
// server.path_view is container) — the same root every other gofer-side path uses.
func (s *Server) workflowTemplateDirs(projectKey string) (projectDir, globalDir string) {
	if projectKey != "" {
		if cfg := s.jobs.Config(); cfg != nil {
			projectDir = cfg.ExecPath(cfg.Projects[projectKey])
		}
	}
	if dir, err := config.ConfigDir(); err == nil {
		globalDir = filepath.Join(dir, "workflows")
	}
	return projectDir, globalDir
}

func (s *Server) handleListWorkflowTemplates(c *rux.Context) {
	// Filter: ?project= selects the project's own template directory; a restricted caller
	// reads only its projects' (the global templates need no project).
	if p := c.Query("project"); p != "" && !s.requireProjectScope(c, p) {
		return
	}
	projectDir, globalDir := s.workflowTemplateDirs(c.Query("project"))
	templates := workflow.LookupWorkflowTemplates(projectDir, globalDir)
	if name := c.Param("name"); name != "" {
		for _, tpl := range templates {
			if tpl.Name == name {
				c.JSON(http.StatusOK, tpl)
				return
			}
		}
		writeError(c, http.StatusNotFound, "unknown workflow template", name)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"templates": templates})
}

// handleRenderWorkflowTemplate serves POST /v1/workflow-templates/{name}/render:
// it applies {vars} (+ declared defaults) to the template and returns the
// executable spec WITHOUT submitting it, so the web wizard can preview the steps.
// Template errors (missing required var, unknown var, unresolved placeholder) are 400.
func (s *Server) handleRenderWorkflowTemplate(c *rux.Context) {
	var req struct {
		Vars map[string]string `json:"vars"`
	}
	if err := c.BindJSON(&req); err != nil && err.Error() != "EOF" {
		writeError(c, http.StatusBadRequest, "invalid render request", err.Error())
		return
	}
	if req.Vars == nil {
		req.Vars = map[string]string{}
	}
	// BodyProject: the project var picks the project's template directory; a restricted
	// caller names only its own.
	if p := req.Vars["project"]; p != "" && !s.requireProjectScope(c, p) {
		return
	}
	projectDir, globalDir := s.workflowTemplateDirs(req.Vars["project"])
	spec, err := workflow.ResolveWorkflowTemplate(c.Param("name"), req.Vars, projectDir, globalDir)
	if err != nil {
		writeError(c, http.StatusBadRequest, "workflow template rejected", err.Error())
		return
	}
	// Preview the same default project the submit would use.
	if spec.Project == "" {
		spec.Project = req.Vars["project"]
	}
	c.JSON(http.StatusOK, spec)
}

// handleGetWorkflow returns a workflow header + its step chain; an unknown id is
// a 404. InHandler(job.read.others, deny) on the workflow's own caller_id (plan T1.5a).
func (s *Server) handleGetWorkflow(c *rux.Context) {
	id := c.Param("id")
	wf, ok, err := s.workflow.GetWorkflow(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get workflow failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown workflow", "no workflow with id "+id)
		return
	}
	if !s.mayReadOwnedRow(c, wf.CallerID) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	steps, err := s.workflow.WorkflowSteps(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list workflow steps failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, workflowDetail{
		ID: wf.ID, Title: wf.Title, Status: wf.Status,
		CurrentStep: wf.CurrentStep, TotalSteps: wf.TotalSteps,
		CallerID: wf.CallerID, Error: wf.Error,
		CreatedAt: wf.CreatedAt, UpdatedAt: wf.UpdatedAt,
		RerunOf: wf.RerunOf,
		Steps:   steps,
	})
}

// handleExportWorkflow returns a workflow's WorkflowSpec reconstructed from its
// persisted spec_json, with credential-looking values stripped (T4.1, E18 + SR403). The
// body is a runnable template: re-POST it to /v1/workflows (or `workflow run <file>`) to
// reproduce the chain. An unknown id is a 404. When anything was redacted the response
// carries an X-Gofer-Redacted: 1 header so the caller can warn that a placeholder must be
// replaced before the export is re-run.
func (s *Server) handleExportWorkflow(c *rux.Context) {
	id := c.Param("id")
	if wf, ok, err := s.workflow.GetWorkflow(id); err == nil && ok && !s.mayReadOwnedRow(c, wf.CallerID) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	spec, ok, redacted, err := s.workflow.ExportWorkflow(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "export workflow failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown workflow", "no workflow with id "+id)
		return
	}
	if redacted {
		c.SetHeader("X-Gofer-Redacted", "1")
	}
	c.JSON(http.StatusOK, spec)
}

// handleListWorkflows returns one page of workflow headers. Query params: status (exact
// match) plus limit / offset (a non-numeric value falls back to 0; no limit keeps the
// store default, so a caller that passes neither still gets the whole capped list). The
// response always carries the non-nil page array plus total — how many rows the same
// filter matches — so the web pager can render "page x of y" without fetching everything,
// and an empty result serialises as {"workflows":[],"total":0}.
//
// It is the FILTER form of job.read.others: without the permission a user-class caller sees
// only its own workflows (plan T1.5a). That caller restriction is pushed into the query, so
// a page is cut after it instead of being narrowed afterwards.
func (s *Server) handleListWorkflows(c *rux.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	filter := jobstore.WorkflowListFilter{Status: c.Query("status")}
	if !s.mayListAllJobs(c) {
		// 只看自己的：显式带上 CallerID（空 id 的调用方也只看空 id 的行，不等于不过滤）。
		filter.CallerID = callerFromCtx(c)
		filter.RestrictCaller = true
	}
	if ceil := s.ceilingOf(c); ceil != nil && len(ceil.Projects) > 0 {
		filter.ProjectKeys = append(filter.ProjectKeys, ceil.Projects...)
	}
	list, total, err := s.workflow.ListWorkflowsPage(filter, limit, offset)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list workflows failed", err.Error())
		return
	}
	// The workflow project filter is persisted and pushed into both SQL queries, so a
	// ceiling-restricted caller's total and page are both limited to visible projects.
	out := make([]workflowSummary, 0, len(list))
	for _, wf := range list {
		out = append(out, toWorkflowSummary(wf))
	}
	c.JSON(http.StatusOK, map[string]any{"workflows": out, "total": total})
}

// handleListWorkflowEvents returns a workflow's append-only lifecycle events (P1)
// in seq order. An unknown workflow id is a 404 (consistent with handleGetWorkflow).
// The optional ?since=<seq> returns only events strictly after that cursor
// (incremental poll). The list is always a non-nil array, so an empty result
// serialises as {"events":[]}.
func (s *Server) handleListWorkflowEvents(c *rux.Context) {
	id := c.Param("id")
	if wf, ok, err := s.workflow.GetWorkflow(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get workflow failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown workflow", "no workflow with id "+id)
		return
	} else if !s.mayReadOwnedRow(c, wf.CallerID) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	// since 非数值 -> 0 -> 不过滤（仿 job events 的容错）。
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	events, err := s.workflow.ListWorkflowEvents(id, since)
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
// current step's job). InHandler(job.cancel.others, deny) on the workflow's caller_id
// (plan T1.5a). It is a stable no-op for an already-terminal workflow and
// a 404 for an unknown id. Returns the updated header snapshot.
func (s *Server) handleCancelWorkflow(c *rux.Context) {
	id := c.Param("id")
	if wf, ok, err := s.workflow.GetWorkflow(id); err == nil && ok && !s.mayCancelOwnedRow(c, wf.CallerID) {
		s.denyJobAction(c, authz.PermJobCancelOthers)
		return
	}
	if err := s.workflow.CancelWorkflow(id); err != nil {
		// The only CancelWorkflow error is an unknown id (terminal is a no-op).
		writeError(c, http.StatusNotFound, "unknown workflow", err.Error())
		return
	}
	wf, _, _ := s.workflow.GetWorkflow(id)
	c.JSON(http.StatusOK, toWorkflowSummary(wf))
}

// handlePickWorkflow records the selected fan of a join=pick step and advances
// the workflow. InHandler(job.manage.others, job.submit, deny): a pick starts the next
// step's job (plan T1.5a). Merge remains a separate worktree endpoint.
func (s *Server) handlePickWorkflow(c *rux.Context) {
	var req pickWorkflowRequest
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid pick request", err.Error())
		return
	}
	if wf, ok, err := s.workflow.GetWorkflow(c.Param("id")); err == nil && ok && !s.mayManageOwnedRow(c, wf.CallerID, true) {
		s.denyJobAction(c, authz.PermJobManageOthers, authz.PermJobSubmit)
		return
	}
	wf, err := s.workflow.PickWorkflowFan(c.Param("id"), req.Step, req.Fan)
	if err != nil {
		writeError(c, submitStatus(err), "workflow pick rejected", err.Error())
		return
	}
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
	RerunOf     string `json:"rerun_of,omitempty"`
}

// toWorkflowSummary projects a jobstore.Workflow onto the API summary shape.
func toWorkflowSummary(wf jobstore.Workflow) workflowSummary {
	return workflowSummary{
		ID: wf.ID, Title: wf.Title, Status: wf.Status,
		CurrentStep: wf.CurrentStep, TotalSteps: wf.TotalSteps,
		CallerID: wf.CallerID, Error: wf.Error,
		CreatedAt: wf.CreatedAt, UpdatedAt: wf.UpdatedAt,
		RerunOf: wf.RerunOf,
	}
}

// rerunWorkflowRequest is the POST /v1/workflows/{id}/rerun body: mode ("failed" or
// "from") and, for "from", the step (number or name) it starts at.
type rerunWorkflowRequest struct {
	Mode string `json:"mode"`
	Step string `json:"step"`
}

// handleRerunWorkflow serves POST /v1/workflows/{id}/rerun: it trims the finished
// workflow's stored spec (`failed` = only the failed steps / fans, `from` = that step
// and every later one) and submits it as a NEW workflow whose rerun_of is the original
// (design §一·7). The original is left untouched. A running workflow can not be re-run
// (400): its outcome is not known yet.
func (s *Server) handleRerunWorkflow(c *rux.Context) {
	var req rerunWorkflowRequest
	if err := c.BindJSON(&req); err != nil && err.Error() != "EOF" {
		writeError(c, http.StatusBadRequest, "invalid rerun request", err.Error())
		return
	}
	id := c.Param("id")
	if wf, ok, err := s.workflow.GetWorkflow(id); err == nil && ok && !s.mayManageOwnedRow(c, wf.CallerID, true) {
		s.denyJobAction(c, authz.PermJobManageOthers, authz.PermJobSubmit)
		return
	}
	nw, ok, err := s.workflow.RerunWorkflow(id, req.Mode, req.Step, callerFromCtx(c))
	if err != nil {
		writeError(c, submitStatus(err), "workflow rerun rejected", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown workflow", "no workflow with id "+id)
		return
	}
	c.JSON(http.StatusOK, toWorkflowSummary(nw))
}
