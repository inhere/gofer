// Package mcpserver exposes the gofer control plane as a stdio MCP server
// (plan P8). It reuses the same job.Service / project / agent registries as the
// HTTP control plane, so the MCP tools never duplicate execution logic — they
// are a thin, structured-IO wrapper over the existing contracts.
//
// All tool input/output structs use snake_case json tags: the SDK infers the
// tool input/output JSON schema from these tags, so the MCP schema stays aligned
// with the HTTP API field names (e.g. project_key, exit_code, result_dir).
//
// This package must NOT import internal/commands (commands wires mcpserver into
// the `mcp` CLI command, so importing back would create a cycle). It depends only
// on internal/job, internal/project, internal/agent and internal/store.
package mcpserver

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/store"
)

// defaultLogTailBytes caps a tail_log response when the caller passes max_bytes
// <= 0 (whole-file is intentionally not the default to avoid huge payloads over
// stdio). It mirrors the HTTP log endpoint cap (httpapi.maxLogTailBytes).
const defaultLogTailBytes = 256 * 1024

// New builds an MCP server whose bridge_* tools are backed by the given Backend
// (localBackend for the in-process standalone path, clientBackend for forwarding
// to a central serve — E28). Handlers own input validation + view projection;
// the Backend owns the actual backend access. The server is returned
// unconnected; call Run (or Serve) to start serving over a transport.
func New(b Backend) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "gofer", Version: "v1"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_list_projects",
		Description: "List the registered projects and their agent/runner allowlists.",
	}, listProjectsHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_list_agents",
		Description: "List the configured/built-in agents with their availability probe.",
	}, listAgentsHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_run_job",
		Description: "Submit an agent/exec job in a project and return its initial state (status, id).",
	}, runJobHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_get_job",
		Description: "Get the current state of a job by id.",
	}, getJobHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_tail_log",
		Description: "Return the tail of a job's stdout/stderr log (capped at 256KB by default).",
	}, tailLogHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_cancel_job",
		Description: "Request cancellation of a running job and return its current state.",
	}, cancelJobHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_get_interactions",
		Description: "List a job's running-time interactions (pending questions and their answers).",
	}, getInteractionsHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_answer_interaction",
		Description: "Answer a pending interaction on a running job so the agent can continue.",
	}, answerInteractionHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_get_artifacts",
		Description: "List a finished job's captured artifact files (name/size/mtime under its result dir).",
	}, getArtifactsHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_get_result",
		Description: "Get a finished job's structured result.json content (E6), as a raw JSON string.",
	}, getResultHandler(b))

	// E36 driver-agent identity/mailbox (4 tools). MCP is one-way (tools only); the
	// driver agent achieves two-way collaboration by registering then polling its
	// inbox through the central serve.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_register",
		Description: "Register this agent (name+role) to the central serve; returns agent_id+agent_token for inbox ops.",
	}, registerAgentHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_poll_inbox",
		Description: "Poll this agent's inbox for unread messages (refreshes presence heartbeat). Set ack=false to peek.",
	}, pollInboxHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_post_message",
		Description: "Send a message/task to another agent (by agent_id, role:<name>, or broadcast). Returns delivered count.",
	}, postMessageHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_list_presence",
		Description: "List online agents (presence registry) with role/project/status. Optional role/project filters.",
	}, listPresenceHandler(b))

	// E25: cross-job pending interactions, for a supervisor agent to discover
	// questions awaiting an answer (then answer via bridge_answer_interaction).
	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_list_pending_interactions",
		Description: "List pending interactions across active jobs (for a supervisor agent to discover questions awaiting an answer).",
	}, listPendingInteractionsHandler(b))

	return s
}

// Serve builds the MCP server over the given Backend and runs it over stdio. It
// blocks until stdin EOF or ctx cancellation. Nothing is written to stdout
// outside the MCP protocol (stdout is the transport), so callers must not print
// to stdout either.
func Serve(ctx context.Context, b Backend) error {
	return New(b).Run(ctx, &mcp.StdioTransport{})
}

// NewLocal is the compatibility constructor for the in-process (standalone) path:
// it wires a localBackend over the given registries/job service and returns the
// MCP server. pres backs the E36 presence tools (nil in presence-less fixtures).
// Equivalent to New(newLocalBackend(...)).
func NewLocal(jobs *job.Service, projects *project.Registry, agents *agent.Registry, pres *presence.Service) *mcp.Server {
	return New(newLocalBackend(jobs, projects, agents, pres))
}

// ServeLocal builds the MCP server over an in-process localBackend and runs it
// over stdio. Convenience wrapper for the standalone path so callers (commands/
// mcp.go) need not reach for newLocalBackend; equivalent to
// Serve(ctx, newLocalBackend(...)). pres backs the E36 presence tools.
func ServeLocal(ctx context.Context, jobs *job.Service, projects *project.Registry, agents *agent.Registry, pres *presence.Service) error {
	return Serve(ctx, newLocalBackend(jobs, projects, agents, pres))
}

// jobView is the snake_case projection of job.JobResult returned by the job
// tools. It mirrors the HTTP JobResult field names so callers see a consistent
// shape across MCP and HTTP.
type jobView struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	ProjectKey string `json:"project_key"`
	Agent      string `json:"agent"`
	Runner     string `json:"runner"`
	ExitCode   int    `json:"exit_code"`
	Cwd        string `json:"cwd"`
	ResultDir  string `json:"result_dir"`
	StartedAt  int64  `json:"started_at"`
	EndedAt    int64  `json:"ended_at"`
	Error      string `json:"error,omitempty"`
	// SessionID is the底层 agent CLI 会话标识 (session-capture); present when the job
	// injected (claude) or captured (codex) one. Surfaced so MCP callers see the
	// same session detail as `gofer job show` / the web console and can drive resume.
	SessionID string `json:"session_id,omitempty"`
	// Channel / Client are submission provenance (cli/web/mcp/im + originating
	// host/addr); surfaced so MCP callers see the same "who/where submitted" detail.
	Channel string `json:"channel,omitempty"`
	Client  string `json:"client,omitempty"`
}

// toJobView projects a job.JobResult onto the snake_case jobView. It is the
// single conversion point shared by run/get/cancel handlers.
func toJobView(r job.JobResult) jobView {
	return jobView{
		ID:         r.ID,
		Status:     r.Status,
		ProjectKey: r.ProjectKey,
		Agent:      r.Agent,
		Runner:     r.Runner,
		ExitCode:   r.ExitCode,
		Cwd:        r.Cwd,
		ResultDir:  r.ResultDir,
		StartedAt:  r.StartedAt,
		EndedAt:    r.EndedAt,
		Error:      r.Error,
		SessionID:  r.SessionID,
		Channel:    r.Channel,
		Client:     r.Client,
	}
}

// mcpHostname returns os.Hostname() to stamp an MCP submission's Client
// (provenance). A lookup failure yields "" (Submit then leaves it empty).
func mcpHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// --- bridge_list_projects ---------------------------------------------------

// listProjectsInput is intentionally empty; SDK maps an empty struct to an empty
// object input schema.
type listProjectsInput struct{}

type projectEntry struct {
	Key               string   `json:"key"`
	HostPath          string   `json:"host_path"`
	ContainerPath     string   `json:"container_path,omitempty"`
	DefaultAgent      string   `json:"default_agent,omitempty"`
	AllowedAgents     []string `json:"allowed_agents,omitempty"`
	AllowedRunners    []string `json:"allowed_runners,omitempty"`
	AllowExec         bool     `json:"allow_exec"`
	MaxConcurrentJobs int      `json:"max_concurrent_jobs,omitempty"`
}

type listProjectsOutput struct {
	Projects []projectEntry `json:"projects"`
}

func listProjectsHandler(b Backend) mcp.ToolHandlerFor[listProjectsInput, listProjectsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ listProjectsInput) (*mcp.CallToolResult, listProjectsOutput, error) {
		entries, err := b.ListProjects()
		if err != nil {
			return nil, listProjectsOutput{}, err
		}
		return nil, listProjectsOutput{Projects: entries}, nil
	}
}

// --- bridge_list_agents -----------------------------------------------------

type listAgentsInput struct{}

// agentEntry mirrors httpapi.agentView but uses the field name "type" for the
// agent type and a name field, matching the MCP tool contract (name/type/
// available/detail). "detail" carries the version on success or the probe error.
type agentEntry struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

type listAgentsOutput struct {
	Agents []agentEntry `json:"agents"`
}

func listAgentsHandler(b Backend) mcp.ToolHandlerFor[listAgentsInput, listAgentsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ listAgentsInput) (*mcp.CallToolResult, listAgentsOutput, error) {
		entries, err := b.ListAgents()
		if err != nil {
			return nil, listAgentsOutput{}, err
		}
		return nil, listAgentsOutput{Agents: entries}, nil
	}
}

// --- bridge_run_job ---------------------------------------------------------

// runJobInput is the snake_case equivalent of job.JobRequest.
type runJobInput struct {
	ProjectKey string   `json:"project_key"`
	Agent      string   `json:"agent"`
	Runner     string   `json:"runner"`
	Prompt     string   `json:"prompt,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty"`
	Title      string   `json:"title,omitempty"`
	// Role is an optional E35 role preset (fills agent/system_prompt/project/tags
	// when unset); SystemPrompt overrides the role's resident system prompt.
	Role         string `json:"role,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
}

func runJobHandler(b Backend) mcp.ToolHandlerFor[runJobInput, jobView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in runJobInput) (*mcp.CallToolResult, jobView, error) {
		// provenance is injected here (handler) so both backends transparently
		// forward it: MCP channel + the MCP server host name.
		res, err := b.RunJob(job.JobRequest{
			ProjectKey: in.ProjectKey,
			Agent:      in.Agent,
			Runner:     in.Runner,
			Prompt:     in.Prompt,
			Cmd:        in.Cmd,
			Cwd:        in.Cwd,
			TimeoutSec: in.TimeoutSec,
			Title:      in.Title,
			// E35 role preset + optional system prompt override (resolved server-side).
			Role:         in.Role,
			SystemPrompt: in.SystemPrompt,
			// 提交来源（provenance）：MCP 渠道 + MCP server 所在主机名。
			Channel: "mcp",
			Client:  mcpHostname(),
		})
		if err != nil {
			return nil, jobView{}, err
		}
		return nil, toJobView(res), nil
	}
}

// --- bridge_get_job ---------------------------------------------------------

type jobIDInput struct {
	ID string `json:"id"`
}

func getJobHandler(b Backend) mcp.ToolHandlerFor[jobIDInput, jobView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, jobView, error) {
		res, err := b.GetJob(in.ID)
		if err != nil {
			return nil, jobView{}, err
		}
		return nil, toJobView(res), nil
	}
}

// --- bridge_tail_log --------------------------------------------------------

type tailLogInput struct {
	ID       string `json:"id"`
	Stream   string `json:"stream,omitempty"`    // "stdout" (default) or "stderr"
	MaxBytes int64  `json:"max_bytes,omitempty"` // <= 0 => defaultLogTailBytes
}

type tailLogOutput struct {
	Text string `json:"text"`
}

func tailLogHandler(b Backend) mcp.ToolHandlerFor[tailLogInput, tailLogOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in tailLogInput) (*mcp.CallToolResult, tailLogOutput, error) {
		// stream validation + default stay in the handler (regularised before the
		// backend sees it); backend only receives the already-vetted stream name.
		stream := in.Stream
		if stream == "" {
			stream = string(store.StreamStdout)
		}
		if stream != string(store.StreamStdout) && stream != string(store.StreamStderr) {
			return nil, tailLogOutput{}, fmt.Errorf("invalid stream %q: must be %q or %q", in.Stream, store.StreamStdout, store.StreamStderr)
		}
		maxBytes := in.MaxBytes
		if maxBytes <= 0 {
			maxBytes = defaultLogTailBytes
		}
		text, err := b.TailLog(in.ID, stream, maxBytes)
		if err != nil {
			return nil, tailLogOutput{}, err
		}
		return nil, tailLogOutput{Text: text}, nil
	}
}

// --- bridge_cancel_job ------------------------------------------------------

func cancelJobHandler(b Backend) mcp.ToolHandlerFor[jobIDInput, jobView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, jobView, error) {
		res, err := b.CancelJob(in.ID)
		if err != nil {
			// The only Cancel error is an unknown job id (terminal jobs are no-ops).
			return nil, jobView{}, err
		}
		return nil, toJobView(res), nil
	}
}

// --- interaction views ------------------------------------------------------

// interactionOptionView is the snake_case projection of job.InteractionOption,
// kept local so the MCP schema never leaks the internal job type.
type interactionOptionView struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// interactionView is the snake_case projection of job.Interaction returned by
// the interaction tools. Like jobView, it mirrors the HTTP API field names so
// MCP and HTTP callers see the same shape.
type interactionView struct {
	ID         string                  `json:"id"`
	JobID      string                  `json:"job_id"`
	Type       string                  `json:"type"`
	Prompt     string                  `json:"prompt"`
	Options    []interactionOptionView `json:"options,omitempty"`
	Status     string                  `json:"status"`
	Answer     string                  `json:"answer,omitempty"`
	CreatedAt  int64                   `json:"created_at"`
	AnsweredAt int64                   `json:"answered_at,omitempty"`
}

// toInteractionView projects a job.Interaction onto the snake_case
// interactionView. Single conversion point shared by the interaction handlers.
func toInteractionView(it job.Interaction) interactionView {
	var opts []interactionOptionView
	if len(it.Options) > 0 {
		opts = make([]interactionOptionView, 0, len(it.Options))
		for _, o := range it.Options {
			opts = append(opts, interactionOptionView{Value: o.Value, Label: o.Label})
		}
	}
	return interactionView{
		ID:         it.ID,
		JobID:      it.JobID,
		Type:       it.Type,
		Prompt:     it.Prompt,
		Options:    opts,
		Status:     it.Status,
		Answer:     it.Answer,
		CreatedAt:  it.CreatedAt,
		AnsweredAt: it.AnsweredAt,
	}
}

// --- bridge_get_interactions ------------------------------------------------

type getInteractionsOutput struct {
	Interactions []interactionView `json:"interactions"`
}

func getInteractionsHandler(b Backend) mcp.ToolHandlerFor[jobIDInput, getInteractionsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, getInteractionsOutput, error) {
		list, err := b.GetInteractions(in.ID)
		if err != nil {
			return nil, getInteractionsOutput{}, err
		}
		// Always emit a non-nil array (unknown jobs yield an empty list).
		out := getInteractionsOutput{Interactions: make([]interactionView, 0, len(list))}
		for _, it := range list {
			out.Interactions = append(out.Interactions, toInteractionView(it))
		}
		return nil, out, nil
	}
}

// --- bridge_list_pending_interactions ---------------------------------------

// listPendingInteractionsInput is intentionally empty (lists across all jobs).
type listPendingInteractionsInput struct{}

func listPendingInteractionsHandler(b Backend) mcp.ToolHandlerFor[listPendingInteractionsInput, getInteractionsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ listPendingInteractionsInput) (*mcp.CallToolResult, getInteractionsOutput, error) {
		list, err := b.ListPendingInteractions()
		if err != nil {
			return nil, getInteractionsOutput{}, err
		}
		out := getInteractionsOutput{Interactions: make([]interactionView, 0, len(list))}
		for _, it := range list {
			out.Interactions = append(out.Interactions, toInteractionView(it))
		}
		return nil, out, nil
	}
}

// --- bridge_answer_interaction ----------------------------------------------

type answerInteractionInput struct {
	ID            string `json:"id"`
	InteractionID string `json:"interaction_id"`
	Answer        string `json:"answer"`
}

func answerInteractionHandler(b Backend) mcp.ToolHandlerFor[answerInteractionInput, interactionView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in answerInteractionInput) (*mcp.CallToolResult, interactionView, error) {
		it, err := b.AnswerInteraction(in.ID, in.InteractionID, in.Answer)
		if err != nil {
			return nil, interactionView{}, err
		}
		return nil, toInteractionView(it), nil
	}
}

// --- bridge_get_artifacts ---------------------------------------------------

// artifactView is the snake_case projection of job.ArtifactItem so the MCP
// schema never leaks the internal job type.
type artifactView struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

type getArtifactsOutput struct {
	Artifacts []artifactView `json:"artifacts"`
}

// getArtifactsHandler returns a job's artifact manifest (E1, D7): the persisted
// manifest when present, else a live scan of the result dir (mirroring the HTTP
// list endpoint). An unknown job is a tool error; a job with no artifacts yields
// a non-nil empty array.
func getArtifactsHandler(b Backend) mcp.ToolHandlerFor[jobIDInput, getArtifactsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, getArtifactsOutput, error) {
		// The backend resolves the manifest (persisted ArtifactsJSON preferred, else
		// a live scan) and projects to []artifactView; an unknown job is a tool
		// error, a job with no artifacts yields a non-nil empty array.
		arts, err := b.GetArtifacts(in.ID)
		if err != nil {
			return nil, getArtifactsOutput{}, err
		}
		return nil, getArtifactsOutput{Artifacts: arts}, nil
	}
}

// --- bridge_get_result ------------------------------------------------------

type getResultOutput struct {
	// ResultJSON is the raw <result_dir>/result.json content (E6), already valid
	// JSON; empty when the job wrote none. Returned as a string so the caller
	// parses it (mirrors the HTTP JobResult.result_json field).
	ResultJSON string `json:"result_json"`
}

// getResultHandler returns a job's structured result.json (E6, D7). An unknown
// job is a tool error; a job with no result.json yields an empty string.
func getResultHandler(b Backend) mcp.ToolHandlerFor[jobIDInput, getResultOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, getResultOutput, error) {
		s, err := b.GetResult(in.ID)
		if err != nil {
			return nil, getResultOutput{}, err
		}
		return nil, getResultOutput{ResultJSON: s}, nil
	}
}

// --- bridge_register --------------------------------------------------------

type registerAgentInput struct {
	Name    string `json:"name"`
	Role    string `json:"role,omitempty"`
	Project string `json:"project,omitempty"`
}

// The output is presence.RegisterResult directly: it carries snake_case json tags
// (agent_id/agent_token) so the SDK derives a schema aligned with the HTTP API.
func registerAgentHandler(b Backend) mcp.ToolHandlerFor[registerAgentInput, presence.RegisterResult] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in registerAgentInput) (*mcp.CallToolResult, presence.RegisterResult, error) {
		res, err := b.RegisterAgent(in.Name, in.Role, in.Project)
		if err != nil {
			return nil, presence.RegisterResult{}, err
		}
		return nil, res, nil
	}
}

// --- bridge_poll_inbox ------------------------------------------------------

type pollInboxToolInput struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
	// Ack defaults to true (consume) when omitted; set false to peek without
	// marking read. A pointer distinguishes "omitted" from an explicit false.
	Ack *bool `json:"ack,omitempty"`
}

type pollInboxOutput struct {
	Messages []presence.Message `json:"messages"`
}

func pollInboxHandler(b Backend) mcp.ToolHandlerFor[pollInboxToolInput, pollInboxOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in pollInboxToolInput) (*mcp.CallToolResult, pollInboxOutput, error) {
		ack := in.Ack == nil || *in.Ack
		msgs, err := b.PollInbox(in.AgentID, in.AgentToken, ack)
		if err != nil {
			return nil, pollInboxOutput{}, err
		}
		if msgs == nil {
			msgs = []presence.Message{}
		}
		return nil, pollInboxOutput{Messages: msgs}, nil
	}
}

// --- bridge_post_message ----------------------------------------------------

type postMessageToolInput struct {
	FromAgent string `json:"from_agent"`
	To        string `json:"to"`
	Kind      string `json:"kind"`
	Body      string `json:"body,omitempty"`
	Ref       string `json:"ref,omitempty"`
}

type postMessageOutput struct {
	Delivered int `json:"delivered"`
}

func postMessageHandler(b Backend) mcp.ToolHandlerFor[postMessageToolInput, postMessageOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in postMessageToolInput) (*mcp.CallToolResult, postMessageOutput, error) {
		n, err := b.PostMessage(in.FromAgent, in.To, in.Kind, in.Body, in.Ref)
		if err != nil {
			return nil, postMessageOutput{}, err
		}
		return nil, postMessageOutput{Delivered: n}, nil
	}
}

// --- bridge_list_presence ---------------------------------------------------

type listPresenceToolInput struct {
	Role    string `json:"role,omitempty"`
	Project string `json:"project,omitempty"`
}

type listPresenceOutput struct {
	Agents []presence.Agent `json:"agents"`
}

func listPresenceHandler(b Backend) mcp.ToolHandlerFor[listPresenceToolInput, listPresenceOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in listPresenceToolInput) (*mcp.CallToolResult, listPresenceOutput, error) {
		list, err := b.ListPresence(in.Role, in.Project)
		if err != nil {
			return nil, listPresenceOutput{}, err
		}
		if list == nil {
			list = []presence.Agent{}
		}
		return nil, listPresenceOutput{Agents: list}, nil
	}
}
