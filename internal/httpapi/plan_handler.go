package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

var planIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func validExplicitPlanID(id string) bool {
	return len(id) >= jobstore.PlanIDMinLength && planIDPattern.MatchString(id)
}

type planView struct {
	PlanID      string `json:"plan_id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Owner       string `json:"owner,omitempty"`
	Progress    int    `json:"progress,omitempty"`
	// Project is the project this plan's todos are dispatched into (PLAN-02 P2).
	Project string `json:"project,omitempty"`
	// Paused holds the chain advance (PLAN-03); BlockedTodo names the item a failed
	// chain job parked the plan on ("" = not blocked, and status is then never
	// `blocked`).
	Paused      bool   `json:"paused,omitempty"`
	BlockedTodo string `json:"blocked_todo,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// planListItem 是 list 响应项：header + 进度汇总（列表进度条数据源，P4/T10）。
// completion 是唯一进度口径（待办优先、无待办回落 job，jobstore.RollupPlanCompletion）；
// counts / todo_counts 是原始汇总。detail（planDetail）另含 jobs/todos/decisions 明细。
type planListItem struct {
	planView
	Counts     jobstore.PlanCounts     `json:"counts"`
	TodoCounts jobstore.PlanTodoCounts `json:"todo_counts"`
	Completion jobstore.PlanCompletion `json:"completion"`
}

func toPlanView(p jobstore.Plan) planView {
	return planView{
		PlanID: p.PlanID, Title: p.Title, Description: p.Description,
		Status: p.Status, Owner: p.Owner, Progress: p.Progress,
		Project:     p.ProjectKey,
		Paused:      p.Paused,
		BlockedTodo: p.BlockedTodo,
		CreatedAt:   p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type todoView struct {
	TodoID string `json:"todo_id"`
	PlanID string `json:"plan_id"`
	JobID  string `json:"job_id,omitempty"`
	Title  string `json:"title"`
	Done   bool   `json:"done"`
	// Lifecycle fields (Part C §C2): status pending|ready|doing|done|skipped with
	// auto-stamped transition times and a short outcome note.
	Status    string `json:"status"`
	StartedAt int64  `json:"started_at,omitempty"`
	DoneAt    int64  `json:"done_at,omitempty"`
	Note      string `json:"note,omitempty"`
	Sort      int    `json:"sort,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	// PLAN-02 P2 dispatch fields: the agent this item is assigned to, the project it
	// runs in (overriding the plan's), the task book + vars, its verify step, review
	// gate, runner, cwd and timeout — and dispatch_error, the reason the last dispatch
	// attempt started nothing.
	Assignee      string            `json:"assignee,omitempty"`
	Project       string            `json:"project,omitempty"`
	Template      string            `json:"template,omitempty"`
	Vars          map[string]string `json:"vars,omitempty"`
	Verify        []string          `json:"verify,omitempty"`
	Review        bool              `json:"review,omitempty"`
	Runner        string            `json:"runner,omitempty"`
	Cwd           string            `json:"cwd,omitempty"`
	TimeoutSec    int               `json:"timeout_sec,omitempty"`
	DispatchError string            `json:"dispatch_error,omitempty"`
	// PLAN-03 chain fields: After lists the items this one waits for, Auto whether the
	// chain may start it, Cmd the argv of an exec item.
	After []string `json:"after,omitempty"`
	Auto  bool     `json:"auto"`
	Cmd   []string `json:"cmd,omitempty"`
	// Jobs are the runs attached to this todo (jobs.todo_id, SUP-01 C), newest
	// first — the plan view shows them under the item instead of asking the client
	// for one jobs query per todo. Empty for an item nobody has run.
	Jobs []todoJobView `json:"jobs,omitempty"`
}

// todoJobView is one job attached to a todo: enough to link to it and see how it
// went at a glance (id/status/agent/duration).
type todoJobView struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Agent  string `json:"agent,omitempty"`
	// StartedAt/EndedAt are unix seconds (EndedAt 0 while the run is live) and
	// DurationSec is how long it took (or has been running) — the "用时" the plan
	// view shows.
	StartedAt   int64 `json:"started_at,omitempty"`
	EndedAt     int64 `json:"ended_at,omitempty"`
	DurationSec int64 `json:"duration_sec,omitempty"`
}

func toTodoJobView(rec jobstore.JobRecord, now int64) todoJobView {
	end := rec.EndedAt
	if end == 0 {
		end = now
	}
	dur := end - rec.StartedAt
	if dur < 0 {
		dur = 0
	}
	return todoJobView{
		ID: rec.ID, Status: rec.Status, Agent: rec.Agent,
		StartedAt: rec.StartedAt, EndedAt: rec.EndedAt, DurationSec: dur,
	}
}

// todoJobsByTodo loads the jobs attached to each of a plan's todos (one query per
// todo, bounded — a plan has tens of items, not thousands). A failure leaves that
// item's list empty rather than failing the whole plan view: the checklist itself
// is what the page is for.
func (s *Server) todoJobsByTodo(todos []jobstore.PlanTodo) map[string][]todoJobView {
	now := time.Now().Unix()
	out := make(map[string][]todoJobView, len(todos))
	for _, t := range todos {
		recs, err := s.jobs.Meta().ListJobsByTodo(t.TodoID, todoJobsLimit)
		if err != nil {
			slog.Warn("plan view: list todo jobs", "todo_id", t.TodoID, "err", err)
			continue
		}
		views := make([]todoJobView, 0, len(recs))
		for _, rec := range recs {
			views = append(views, toTodoJobView(rec, now))
		}
		out[t.TodoID] = views
	}
	return out
}

// todoJobsLimit bounds the runs listed per todo (the newest ones are the ones a
// reader acts on; the full history is `job list`).
const todoJobsLimit = 10

func toTodoView(t jobstore.PlanTodo) todoView {
	return todoView{
		TodoID: t.TodoID, PlanID: t.PlanID, JobID: t.JobID, Title: t.Title,
		Done: t.Done, Status: t.Status, StartedAt: t.StartedAt, DoneAt: t.DoneAt,
		Note: t.Note, Sort: t.Sort, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Assignee: t.Assignee, Project: t.ProjectKey, Template: t.Template,
		Vars: t.Vars, Verify: t.Verify, Review: t.Review, Runner: t.Runner,
		Cwd: t.Cwd, TimeoutSec: t.TimeoutSec, DispatchError: t.DispatchError,
		After: t.After, Auto: t.Auto, Cmd: t.Cmd,
	}
}

type createPlanReq struct {
	PlanID      string `json:"plan_id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Project is the project this plan's todos are dispatched into (PLAN-02 P2); a
	// todo may still override it. Empty = the plan names none.
	Project string `json:"project,omitempty"`
}

// updatePlanReq is the PATCH /v1/plans/{id} body (P6): move a plan along its
// lifecycle. status is required; progress is optional (nil = keep current).
// 系统不自动推进 plan 状态（C2：plan 是纯归组），全部由调用方显式置。
type updatePlanReq struct {
	Status   string `json:"status"`
	Progress *int   `json:"progress,omitempty"`
}

// validPlanStatus 白名单：jobstore.SetPlanStatus 不校验取值，必须在入口挡住。
// `blocked` 也在列：它是 PLAN-03 的链停状态，由推进逻辑写入，也允许人工把 plan
// 直接标成 blocked（与 open 一样是"等人处理"的显式表达）。
func validPlanStatus(s string) bool {
	switch s {
	case jobstore.PlanOpen, jobstore.PlanActive, jobstore.PlanDone, jobstore.PlanArchived, jobstore.PlanBlocked:
		return true
	}
	return false
}

func (s *Server) handleCreatePlan(c *rux.Context) {
	var body createPlanReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	planID := strings.TrimSpace(body.PlanID)
	if planID != "" && !validExplicitPlanID(planID) {
		writeError(c, http.StatusBadRequest, "invalid plan id", "plan id must be at least 9 characters (short ids are too easy to collide) and contain only A-Z, a-z, 0-9, '.', '_', ':' or '-'")
		return
	}
	if planID == "" {
		planID = "plan-" + time.Now().Format(job.JobIDLayout) + "-" + job.RandomSuffix()
	}
	now := time.Now().Unix()
	p := jobstore.Plan{
		PlanID: planID, Title: body.Title, Description: body.Description,
		Status: jobstore.PlanOpen, Owner: callerFromCtx(c),
		ProjectKey: strings.TrimSpace(body.Project),
		CreatedAt:  now, UpdatedAt: now,
	}
	if _, ok, _ := s.jobs.Meta().GetPlan(planID); ok {
		writeError(c, http.StatusConflict, "plan already exists", "plan already exists")
		return
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
	ids := make([]string, len(list))
	for i, p := range list {
		ids[i] = p.PlanID
	}
	// One grouped query for every listed plan's todos (no per-plan todo query).
	todoCounts, err := s.jobs.Meta().PlanTodoCountsByPlan(ids)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "plan todo counts failed", err.Error())
		return
	}
	out := make([]planListItem, 0, len(list))
	for _, p := range list {
		raw, cErr := s.jobs.Meta().PlanJobStatusCounts(p.PlanID)
		if cErr != nil {
			writeError(c, http.StatusInternalServerError, "plan counts failed", cErr.Error())
			return
		}
		jc, tc := jobstore.RollupPlanCounts(raw), todoCounts[p.PlanID]
		out = append(out, planListItem{
			planView:   toPlanView(p),
			Counts:     jc,
			TodoCounts: tc,
			Completion: jobstore.RollupPlanCompletion(jc, tc),
		})
	}
	c.JSON(http.StatusOK, map[string]any{"plans": out})
}

// planUsageView is the plan's token/cost roll-up on the wire (PLAN-02 P2): what the
// plan's jobs reported, in total and per agent. jobs counts every attached job (one
// that reported no usage still ran); the sums only see the rows that carried numbers.
type planUsageView struct {
	Jobs        int                       `json:"jobs"`
	TotalTokens int64                     `json:"total_tokens"`
	CostUSD     float64                   `json:"cost_usd"`
	ByAgent     map[string]planUsageAgent `json:"by_agent"`
}

type planUsageAgent struct {
	Jobs         int     `json:"jobs"`
	TotalTokens  int64   `json:"total_tokens"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

func toPlanUsageView(u jobstore.PlanUsage) planUsageView {
	byAgent := make(map[string]planUsageAgent, len(u.ByAgent))
	for key, a := range u.ByAgent {
		byAgent[key] = planUsageAgent{
			Jobs: a.Jobs, TotalTokens: a.TotalTokens, InputTokens: a.InputTokens,
			OutputTokens: a.OutputTokens, CostUSD: a.CostUSD,
		}
	}
	return planUsageView{Jobs: u.Jobs, TotalTokens: u.TotalTokens, CostUSD: u.CostUSD, ByAgent: byAgent}
}

type planDetail struct {
	planView
	Counts     jobstore.PlanCounts     `json:"counts"`
	TodoCounts jobstore.PlanTodoCounts `json:"todo_counts"`
	Completion jobstore.PlanCompletion `json:"completion"`
	Usage      planUsageView           `json:"usage"`
	Jobs       []job.JobResult         `json:"jobs"`
	Todos      []todoView              `json:"todos"`
	Decisions  []decisionView          `json:"decisions"`
	// Leader is the plan's leader-round state (MCP-05 阶段 B): the cap, how many rounds
	// the plan has spent and the last leader job it started — the "leader 第 N/M 轮" line
	// the plan page shows, plus the link to the round's job. Absent (omitted) for a plan
	// whose leader round is off, which is the default.
	Leader *planLeaderView `json:"leader,omitempty"`
}

// planLeaderView is the leader-round state of one plan (MCP-05 阶段 B). Round counts the
// rounds SPENT (a leader job was started), so "round 0" means the leader has not been
// woken for this plan yet; MaxRounds is the configured cap.
type planLeaderView struct {
	Round     int    `json:"round"`
	MaxRounds int    `json:"max_rounds"`
	LastJobID string `json:"last_job_id,omitempty"`
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
	jobsByTodo := s.todoJobsByTodo(todos)
	for _, t := range todos {
		tv := toTodoView(t)
		tv.Jobs = jobsByTodo[t.TodoID]
		todoViews = append(todoViews, tv)
	}
	jc, tc := jobstore.RollupPlanCounts(raw), jobstore.CountTodos(todos)
	// Additive: inline the plan's decisions so PlanDetail gets everything in one
	// request on the existing poll (decision channel, Part C §C3).
	decisions, err := s.jobs.Meta().ListDecisions("", id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list plan decisions failed", err.Error())
		return
	}
	decisionViews := make([]decisionView, 0, len(decisions))
	for _, d := range decisions {
		decisionViews = append(decisionViews, toDecisionView(*d))
	}
	// PLAN-02 P2: the plan's own usage roll-up, so the plan page answers "what has this
	// cost so far" without opening every job of it.
	usage, err := s.jobs.Meta().PlanUsage(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "plan usage failed", err.Error())
		return
	}
	var leader *planLeaderView
	if st := s.jobs.LeaderStatus(id); st.Enabled {
		leader = &planLeaderView{Round: st.Round, MaxRounds: st.MaxRounds, LastJobID: st.LastJobID}
	}
	c.JSON(http.StatusOK, planDetail{
		planView:   toPlanView(p),
		Counts:     jc,
		TodoCounts: tc,
		Completion: jobstore.RollupPlanCompletion(jc, tc),
		Usage:      toPlanUsageView(usage),
		Jobs:       jobs,
		Todos:      todoViews,
		Decisions:  decisionViews,
		Leader:     leader,
	})
}

func (s *Server) handleUpdatePlan(c *rux.Context) {
	id := c.Param("id")
	var body updatePlanReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	status := strings.TrimSpace(body.Status)
	if !validPlanStatus(status) {
		writeError(c, http.StatusBadRequest, "invalid status",
			"status must be one of open/active/done/archived")
		return
	}
	// SetPlanStatus 用裸 UPDATE、不看 affected rows：不存在的 plan 会「假成功」。
	// 故先 GetPlan 判存在（同 handleAttachPlanJob 的前置模式）。
	if _, ok, err := s.jobs.Meta().GetPlan(id); err != nil {
		writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown plan", "no plan with id "+id)
		return
	}
	progress := -1 // <0 = 保持原 progress（plans.go:112）
	if body.Progress != nil {
		progress = *body.Progress
	}
	if err := s.jobs.Meta().SetPlanStatus(id, status, progress); err != nil {
		writeError(c, http.StatusInternalServerError, "update plan failed", err.Error())
		return
	}
	// PLAN-03: keep "status=blocked ⟺ blocked_todo is set" true — a human moving the
	// plan off `blocked` by hand also clears the item it parked on (the reverse, an
	// explicit status=blocked with no item, is a plan-level note and leaves it empty).
	if status != jobstore.PlanBlocked {
		if err := s.jobs.Meta().ClearPlanBlocked(id); err != nil {
			writeError(c, http.StatusInternalServerError, "update plan failed", err.Error())
			return
		}
	}
	p, ok, err := s.jobs.Meta().GetPlan(id)
	if err != nil || !ok {
		writeError(c, http.StatusInternalServerError, "reload plan failed", "")
		return
	}
	c.JSON(http.StatusOK, toPlanView(p))
}

type attachJobReq struct {
	JobID string `json:"job_id"`
}

// handleRunPlan is POST /v1/plans/{id}/run (PLAN-03): start the chain — queue every
// pending item whose dependencies are satisfied and that has an assignee, release a
// pause/block first, and return the plan as it stands. The response is the plan header
// (the same shape PATCH returns), so a caller sees the status/paused/blocked it just
// produced; the items and their jobs are on GET /v1/plans/{id}.
func (s *Server) handleRunPlan(c *rux.Context) {
	p, err := s.jobs.RunPlan(c.Param("id"), callerFromCtx(c))
	if err != nil {
		writeError(c, planActionStatus(err), "run plan failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toPlanView(p))
}

// handlePausePlan is POST /v1/plans/{id}/pause (PLAN-03): hold the automatic chain
// advance. Items already running are NOT cancelled — the pause is about what starts
// next.
func (s *Server) handlePausePlan(c *rux.Context) {
	p, err := s.jobs.PausePlan(c.Param("id"))
	if err != nil {
		writeError(c, planActionStatus(err), "pause plan failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toPlanView(p))
}

// handleResumePlan is POST /v1/plans/{id}/resume (PLAN-03): release a pause and a
// block, then advance the chain from wherever it stands.
func (s *Server) handleResumePlan(c *rux.Context) {
	p, err := s.jobs.ResumePlan(c.Param("id"), callerFromCtx(c))
	if err != nil {
		writeError(c, planActionStatus(err), "resume plan failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toPlanView(p))
}

// planActionStatus maps the plan actions' errors: an unknown plan is a 404, anything
// else the service refused is the caller's to fix (400).
func planActionStatus(err error) int {
	if errors.Is(err, job.ErrInvalidRequest) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
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

// addTodoReq is POST /v1/plans/{id}/todos. The PLAN-02 P2 dispatch fields may be set at
// creation too, but the item is created `pending`: creating an item is planning, and
// only `ready` (or an explicit dispatch) starts work.
type addTodoReq struct {
	Title string `json:"title"`
	JobID string `json:"job_id,omitempty"`
	Note  string `json:"note,omitempty"`
	Sort  int    `json:"sort,omitempty"`
	jobstore.TodoPatch
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
		TodoID: "todo-" + now.Format(job.JobIDLayout) + "-" + job.RandomSuffix(),
		PlanID: id,
		JobID:  strings.TrimSpace(body.JobID),
		Title:  body.Title,
		Status: jobstore.TodoPending,
		Note:   body.Note,
		Sort:   body.Sort,
		// PLAN-03: a new item joins the chain by default (the body may still say
		// auto=false through the patch below).
		Auto:      true,
		CreatedAt: now.Unix(),
		UpdatedAt: now.Unix(),
	}
	t.ApplyTodoPatch(body.TodoPatch)
	if err := s.jobs.Meta().InsertTodo(t); err != nil {
		writeError(c, http.StatusInternalServerError, "add todo failed", err.Error())
		return
	}
	_ = s.jobs.Meta().TouchPlan(id)
	c.JSON(http.StatusOK, toTodoView(t))
}

// updateTodoReq moves a todo along its lifecycle and/or updates its note.
// status (pending|ready|doing|done|skipped) wins over the legacy done flag; done is a
// *bool so an old client's {"done":...} body keeps working while a status-only
// or note-only body doesn't accidentally reset done=false. append_note appends
// a line to the current note (atomically, newline-separated) and is mutually
// exclusive with note (overwrite).
//
// The PLAN-02 P2 dispatch fields (assignee/project/template/vars/verify/review/runner/
// cwd/timeout_sec) travel in the same body — one update describes the item AND the
// request it dispatches with.
type updateTodoReq struct {
	Done       *bool   `json:"done,omitempty"`
	Status     string  `json:"status,omitempty"`
	Note       *string `json:"note,omitempty"`
	AppendNote string  `json:"append_note,omitempty"`
	jobstore.TodoPatch
}

func (s *Server) handleUpdateTodo(c *rux.Context) {
	tid := c.Param("todo_id")
	var body updateTodoReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	status := strings.TrimSpace(body.Status)
	if status == "" && body.Done != nil {
		// Legacy二态 body: map onto the lifecycle.
		status = jobstore.TodoPending
		if *body.Done {
			status = jobstore.TodoDone
		}
	}
	if status != "" && !jobstore.ValidTodoStatus(status) {
		writeError(c, http.StatusBadRequest, "invalid status",
			"status must be one of pending|ready|doing|done|skipped")
		return
	}
	if body.Note != nil && body.AppendNote != "" {
		writeError(c, http.StatusBadRequest, "conflicting note fields",
			"note (overwrite) and append_note are mutually exclusive")
		return
	}
	if status == "" && body.Note == nil && body.AppendNote == "" && body.TodoPatch.Empty() {
		writeError(c, http.StatusBadRequest, "empty update",
			"provide status, done, note, append_note or a dispatch field")
		return
	}
	if !body.TodoPatch.Empty() {
		ok, err := s.jobs.Meta().UpdateTodoPatch(tid, body.TodoPatch)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "update todo failed", err.Error())
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "unknown todo", "no todo with id "+tid)
			return
		}
	}
	if status != "" || body.Note != nil {
		ok, err := s.jobs.Meta().UpdateTodoStatus(tid, status, body.Note)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "update todo failed", err.Error())
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "unknown todo", "no todo with id "+tid)
			return
		}
	}
	if body.AppendNote != "" {
		ok, err := s.jobs.Meta().AppendTodoNote(tid, body.AppendNote)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "append todo note failed", err.Error())
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "unknown todo", "no todo with id "+tid)
			return
		}
	}
	// PLAN-02 P2: the two conditions may arrive in either order, so a write that moved
	// the item to `ready` OR set its assignee is a dispatch trigger (the dispatcher
	// itself re-checks both, and stays silent when the item is not ready yet).
	// PLAN-03: a status write ALSO releases a block and advances the plan's chain (a
	// human marking an item done/skipped moves the plan exactly like a finished job).
	// Re-read after the write: the response must show what the dispatch did to the item.
	if status == jobstore.TodoReady || body.Assignee != nil {
		s.dispatchTodoNow(c, tid)
	}
	if status != "" {
		s.jobs.PlanTodoChanged(tid, status, callerFromCtx(c))
	}
	t, _, err := s.jobs.Meta().GetTodo(tid)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get todo failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toTodoView(t))
}

// dispatchTodoNow runs the PLAN-02 dispatcher for a just-written todo and never fails
// the write: the caller's update IS stored, and a refused dispatch is reported on the
// item itself (dispatch_error) plus the event stream — that is what the design asks
// for, and a 500 here would tell a client its update failed when it did not.
func (s *Server) dispatchTodoNow(c *rux.Context, todoID string) {
	if _, err := s.jobs.MaybeDispatchTodo(todoID, callerFromCtx(c)); err != nil {
		slog.Warn("todo dispatch", "todo_id", todoID, "err", err)
	}
}

// todoDispatchView is the dispatch response (POST /v1/todos/{id}/dispatch): the item
// as it stands now, the job the call started (absent when none was) and the reason.
type todoDispatchView struct {
	Todo       todoView       `json:"todo"`
	Job        *job.JobResult `json:"job,omitempty"`
	Dispatched bool           `json:"dispatched"`
	Reason     string         `json:"reason,omitempty"`
}

// handleDispatchTodo is the EXPLICIT dispatch (PLAN-02 P2): `plan dispatch <todo>` and
// gofer_dispatch_todo. It ignores the item's status — re-running a done item or kicking
// a pending one is exactly what a human reaches for — but still needs an assignee and no
// live job, and it answers with what happened instead of failing the request.
func (s *Server) handleDispatchTodo(c *rux.Context) {
	tid := c.Param("todo_id")
	d, err := s.jobs.DispatchTodo(tid, callerFromCtx(c))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, job.ErrInvalidRequest) {
			// Unknown todo, or no assignee to run: both are the caller's to fix.
			status = http.StatusBadRequest
			if _, ok, gerr := s.jobs.Meta().GetTodo(tid); gerr == nil && !ok {
				status = http.StatusNotFound
			}
		}
		writeError(c, status, "dispatch todo failed", err.Error())
		return
	}
	out := todoDispatchView{Todo: toTodoView(d.Todo), Job: d.Job, Dispatched: d.Job != nil}
	if d.Job == nil {
		out.Reason = d.Reason
	}
	c.JSON(http.StatusOK, out)
}
