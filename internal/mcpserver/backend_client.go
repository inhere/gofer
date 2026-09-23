package mcpserver

import (
	"encoding/json"
	"fmt"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/template"
)

// clientBackend is the remote Backend: every method forwards to a central gofer
// serve over the HTTP client (internal/client). It is the counterpart to
// localBackend (E28 P3) — the gofer_* handlers keep input validation + view
// projection, while this backend turns each call into a /v1/... request. The
// project/agent/artifact views are produced here to be byte-for-byte shape
// compatible with localBackend (same non-nil empty slices, same field mapping)
// so client mode and standalone mode surface identical tool output.
type clientBackend struct {
	cli *client.Client
}

// NewClientBackend wires a Backend that forwards to the central serve at cli.
func NewClientBackend(cli *client.Client) Backend {
	return &clientBackend{cli: cli}
}

// RunJob submits asynchronously and returns the initial (queued/running)
// snapshot, matching localBackend.RunJob's submit semantics.
func (b *clientBackend) RunJob(req job.JobRequest) (job.JobResult, error) {
	return b.cli.SubmitJob(req)
}

// ListTemplates forwards the read to the central serve: the templates live on the
// server's disk, not on this process's.
func (b *clientBackend) ListTemplates(project string) ([]template.Info, error) {
	return b.cli.ListTemplates(project)
}

func (b *clientBackend) GetJob(id string) (job.JobResult, error) {
	return b.cli.GetJob(id)
}

func (b *clientBackend) CancelJob(id string) (job.JobResult, error) {
	return b.cli.CancelJob(id)
}

// RejectJob forwards the refusal to the central serve, whose own auth stamps the
// reviewer (by here is only used by the in-process backend).
func (b *clientBackend) RejectJob(id, note string, resume bool, _ string) (job.ReviewOutcome, error) {
	return b.cli.RejectJob(id, note, resume)
}

// GetResult returns the job's result.json content (the get_job snapshot's
// ResultJSON), mirroring localBackend.GetResult.
func (b *clientBackend) GetResult(id string) (string, error) {
	r, err := b.cli.GetJob(id)
	if err != nil {
		return "", err
	}
	return r.ResultJSON, nil
}

func (b *clientBackend) GetInteractions(id string) ([]job.Interaction, error) {
	return b.cli.GetInteractions(id)
}

func (b *clientBackend) AnswerInteraction(id, iid, answer, responder string) (job.Interaction, error) {
	// Forward the responder (this client's self-registered driver agent_id) so the central
	// serve's answer闸 (P3.1) grades the source server-side (presence/whitelist live there).
	return b.cli.AnswerInteraction(id, iid, answer, responder)
}

func (b *clientBackend) PuntInteraction(id, iid string) error {
	return b.cli.PuntInteraction(id, iid)
}

func (b *clientBackend) ListPendingInteractions() ([]job.Interaction, error) {
	return b.cli.ListPendingInteractions()
}

// TailLog reads the server's legacy byte-tail response and optionally trims it
// further client-side. maxBytes<=0 means "no cap".
func (b *clientBackend) TailLog(id, stream string, maxBytes int64) (string, error) {
	s, err := b.cli.GetLogs(id, stream)
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && int64(len(s)) > maxBytes {
		s = s[int64(len(s))-maxBytes:]
	}
	return s, nil
}

// ListProjects maps the server's project meta into projectEntry. host_path /
// container_path are left empty because the meta endpoint does not expose
// server-side filesystem paths (same as `project list --remote`, E38); AllowExec
// / MaxConcurrentJobs are likewise absent from the meta shape, so they stay zero.
// The slice is always non-nil (matching localBackend).
func (b *clientBackend) ListProjects() ([]projectEntry, error) {
	metas, err := b.cli.ListProjects()
	if err != nil {
		return nil, err
	}
	out := make([]projectEntry, 0, len(metas))
	for _, m := range metas {
		out = append(out, projectEntry{
			Key:            m.Key,
			DefaultAgent:   m.DefaultAgent,
			AllowedAgents:  m.AllowedAgents,
			AllowedRunners: m.AllowedRunners,
		})
	}
	return out, nil
}

// ListAgents maps the server's agent meta (already folded to name/type/available/
// detail by client.ListAgents) 1:1 into agentEntry. Non-nil empty slice matches
// localBackend.
func (b *clientBackend) ListAgents() ([]agentEntry, error) {
	metas, err := b.cli.ListAgents()
	if err != nil {
		return nil, err
	}
	out := make([]agentEntry, 0, len(metas))
	for _, m := range metas {
		out = append(out, agentEntry{
			Name:      m.Name,
			Type:      m.Type,
			Available: m.Available,
			Detail:    m.Detail,
		})
	}
	return out, nil
}

// GetArtifacts fetches the peer job's manifest. client.ListArtifacts returns the
// inner `[{name,size,mtime},...]` array (unwrapped from {"artifacts":[...]}) as
// raw JSON; parse it into the artifactView projection. An empty/absent manifest
// (nil/zero-length raw) yields a non-nil empty slice, matching localBackend.
func (b *clientBackend) GetArtifacts(id string) ([]artifactView, error) {
	raw, err := b.cli.ListArtifacts(id)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return make([]artifactView, 0), nil
	}
	var items []job.ArtifactItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode artifacts manifest: %w", err)
	}
	out := make([]artifactView, 0, len(items))
	for _, it := range items {
		out = append(out, artifactView{Name: it.Name, Size: it.Size, Mtime: it.Mtime})
	}
	return out, nil
}

// --- plan grouping (client 转发中央 serve) -----------------------------------

func (b *clientBackend) CreatePlan(title, description string) (planView, error) {
	// The MCP tool takes no project: an MCP-created plan's items name their own
	// project (gofer_add_todo / gofer_update_todo `project`). The CLI is where
	// `plan create --project` belongs.
	p, err := b.cli.CreatePlan("", title, description, "")
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func (b *clientBackend) AttachJob(planID, jobID string) (planView, error) {
	p, err := b.cli.AttachJob(planID, jobID)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func (b *clientBackend) GetPlan(planID string) (planView, error) {
	p, err := b.cli.GetPlan(planID)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func (b *clientBackend) AddTodo(planID, title, jobID, note string, patch jobstore.TodoPatch) (todoView, error) {
	t, err := b.cli.AddTodo(planID, title, jobID, note, patch)
	if err != nil {
		return todoView{}, err
	}
	return clientTodoToView(t), nil
}

func (b *clientBackend) UpdateTodo(todoID, status string, note *string, appendNote string, patch jobstore.TodoPatch) (todoView, error) {
	// ONE PATCH carries the lifecycle fields and the dispatch patch — the server applies
	// them as one update, so a failure never leaves a half-applied change, and a write
	// that makes the item dispatchable also runs the planner there.
	t, err := b.cli.PatchTodo(todoID, client.TodoUpdate{
		Status: status, Note: note, AppendNote: appendNote, TodoPatch: patch,
	})
	if err != nil {
		return todoView{}, err
	}
	return clientTodoToView(t), nil
}

// RunPlan starts a plan's dependency chain through the central server
// (POST /v1/plans/{id}/run, PLAN-03) — the MCP twin of `plan run`.
func (b *clientBackend) RunPlan(planID string) (planView, error) {
	p, err := b.cli.RunPlan(planID)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func (b *clientBackend) DispatchTodo(todoID string) (todoDispatchView, error) {
	d, err := b.cli.DispatchTodo(todoID)
	if err != nil {
		return todoDispatchView{}, err
	}
	out := todoDispatchView{Todo: clientTodoToView(d.Todo), Dispatched: d.Dispatched}
	if d.Job != nil {
		v := toJobView(*d.Job)
		out.Job = &v
	}
	out.Reason = d.Reason
	return out, nil
}

// CreateWakeup registers a JOB-09 wakeup through the central server
// (POST /v1/jobs/{id}/wakeups) — the MCP twin of `job wakeup create`.
func (b *clientBackend) CreateWakeup(jobID string, spec job.WakeupSpec) (wakeupView, error) {
	w, err := b.cli.CreateWakeup(jobID, spec)
	if err != nil {
		return wakeupView{}, err
	}
	return wakeupView(w), nil
}

// ListWakeups reads a job's wakeups through the central server.
func (b *clientBackend) ListWakeups(jobID string) ([]wakeupView, error) {
	ws, err := b.cli.ListWakeups(jobID)
	if err != nil {
		return nil, err
	}
	out := make([]wakeupView, 0, len(ws))
	for _, w := range ws {
		out = append(out, wakeupView(w))
	}
	return out, nil
}

// SetWakeupEnabled flips a wakeup's switch through the central server.
func (b *clientBackend) SetWakeupEnabled(wakeupID string, enabled bool) (wakeupView, error) {
	w, err := b.cli.SetWakeupEnabled(wakeupID, enabled)
	if err != nil {
		return wakeupView{}, err
	}
	return wakeupView(w), nil
}

func clientPlanToView(p client.Plan) planView {
	pv := planView{
		PlanID:      p.PlanID,
		Title:       p.Title,
		Description: p.Description,
		Status:      p.Status,
		Owner:       p.Owner,
		Progress:    p.Progress,
		Project:     p.Project,
		Paused:      p.Paused,
		BlockedTodo: p.BlockedTodo,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		Jobs:        make([]jobView, 0, len(p.Jobs)),
	}
	if p.Counts != nil {
		pv.Counts = *p.Counts
	}
	for _, j := range p.Jobs {
		pv.Jobs = append(pv.Jobs, toJobView(j))
	}
	pv.Todos = make([]todoView, 0, len(p.Todos))
	for _, t := range p.Todos {
		pv.Todos = append(pv.Todos, clientTodoToView(t))
	}
	return pv
}

func clientTodoToView(t client.Todo) todoView {
	return todoView{
		TodoID: t.TodoID, PlanID: t.PlanID, JobID: t.JobID, Title: t.Title,
		Done: t.Done, Status: t.Status, StartedAt: t.StartedAt, DoneAt: t.DoneAt,
		Note: t.Note, Sort: t.Sort, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Assignee: t.Assignee, Project: t.Project, Template: t.Template,
		Vars: t.Vars, Verify: t.Verify, Review: t.Review, Runner: t.Runner,
		Cwd: t.Cwd, TimeoutSec: t.TimeoutSec, DispatchError: t.DispatchError,
		After: t.After, Auto: t.Auto, Cmd: t.Cmd,
	}
}

// --- decision channel (client 转发中央 serve) --------------------------------

func (b *clientBackend) AskDecision(planID, title, question string, options []string, timeoutSec int64) (jobstore.PlanDecision, error) {
	d, err := b.cli.AskDecision(planID, title, question, options, timeoutSec)
	if err != nil {
		return jobstore.PlanDecision{}, err
	}
	return clientDecisionToStore(d), nil
}

// GetDecision forwards the poll; the central serve's read path applies lazy
// expiry. A 404 (unknown id) surfaces as an error via the client's errorFor —
// for the ask_human polling loop that is a hard anomaly, not a "keep waiting".
func (b *clientBackend) GetDecision(id string) (jobstore.PlanDecision, bool, error) {
	d, err := b.cli.GetDecision(id)
	if err != nil {
		return jobstore.PlanDecision{}, false, err
	}
	return clientDecisionToStore(d), true, nil
}

// clientDecisionToStore maps the client wire view onto the store domain type
// the handler polls. Options are re-marshalled into OptionsJSON for parity with
// the local backend (the ask_human handler itself only reads state/answer).
func clientDecisionToStore(d client.Decision) jobstore.PlanDecision {
	var optionsJSON string
	if len(d.Options) > 0 {
		if raw, err := json.Marshal(d.Options); err == nil {
			optionsJSON = string(raw)
		}
	}
	return jobstore.PlanDecision{
		ID: d.ID, PlanID: d.PlanID, Title: d.Title, Question: d.Question,
		OptionsJSON: optionsJSON, Answer: d.Answer, State: d.State,
		TimeoutSec: d.TimeoutSec, AskedAt: d.AskedAt,
		AnsweredAt: d.AnsweredAt, AnsweredBy: d.AnsweredBy,
	}
}

// --- E36 presence (client 转发中央 serve) ------------------------------------

func (b *clientBackend) RegisterAgent(name, role, project string) (presence.RegisterResult, error) {
	id, tok, err := b.cli.RegisterAgent(name, role, project)
	if err != nil {
		return presence.RegisterResult{}, err
	}
	return presence.RegisterResult{AgentID: id, AgentToken: tok}, nil
}

func (b *clientBackend) PollInbox(agentID, token string, ack bool) ([]presence.Message, error) {
	return b.cli.PollInbox(agentID, token, ack)
}

func (b *clientBackend) PostMessage(from, to, kind, body, ref string) (int, error) {
	return b.cli.PostMessage(from, to, kind, body, ref)
}

func (b *clientBackend) ListPresence(role, project string) ([]presence.Agent, error) {
	return b.cli.ListPresence(role, project)
}

// Comment forwards a comment to the central serve (MCP-05 阶段 A). as_job travels on the
// wire so the SERVER decides the author kind from that job — a client cannot declare
// itself a human.
func (b *clientBackend) Comment(scope, id, body, asJob string) (commentView, error) {
	cm, err := b.cli.PostComment(scope, id, body, asJob)
	if err != nil {
		return commentView{}, err
	}
	return clientCommentView(cm), nil
}

func (b *clientBackend) ListComments(scope, id string) ([]commentView, error) {
	rows, err := b.cli.ListComments(scope, id)
	if err != nil {
		return nil, err
	}
	out := make([]commentView, 0, len(rows))
	for _, cm := range rows {
		out = append(out, clientCommentView(cm))
	}
	return out, nil
}

// clientCommentView projects the client package's wire type onto the tool view.
func clientCommentView(cm client.Comment) commentView {
	v := commentView{
		ID: cm.ID, Scope: cm.Scope, ScopeID: cm.ScopeID,
		Author: cm.Author, AuthorKind: cm.AuthorKind, Body: cm.Body,
		Mentions:       cm.Mentions,
		CreatedAt:      cm.CreatedAt,
		TriggeredJobID: cm.TriggeredJobID,
	}
	if v.Mentions == nil {
		v.Mentions = []string{}
	}
	for _, d := range cm.Dispatched {
		v.Dispatched = append(v.Dispatched, commentDispatchView{Mention: d.Mention, Kind: d.Kind, JobID: d.JobID})
	}
	return v
}
