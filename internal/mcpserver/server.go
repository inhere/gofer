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
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/store"
)

// defaultLogTailBytes caps a tail_log response when the caller passes max_bytes
// <= 0 (whole-file is intentionally not the default to avoid huge payloads over
// stdio). It mirrors the HTTP log endpoint cap (httpapi.maxLogTailBytes).
const defaultLogTailBytes = 256 * 1024

// New builds an MCP server wired to the given registries/job service and
// registers the bridge_* tools. The server is returned unconnected; call
// Run (or Serve) to start serving over a transport.
func New(jobs *job.Service, projects *project.Registry, agents *agent.Registry) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "gofer", Version: "v1"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_list_projects",
		Description: "List the registered projects and their agent/runner allowlists.",
	}, listProjectsHandler(projects))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_list_agents",
		Description: "List the configured/built-in agents with their availability probe.",
	}, listAgentsHandler(agents))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_run_job",
		Description: "Submit an agent/exec job in a project and return its initial state (status, id).",
	}, runJobHandler(jobs))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_get_job",
		Description: "Get the current state of a job by id.",
	}, getJobHandler(jobs))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_tail_log",
		Description: "Return the tail of a job's stdout/stderr log (capped at 256KB by default).",
	}, tailLogHandler(jobs))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_cancel_job",
		Description: "Request cancellation of a running job and return its current state.",
	}, cancelJobHandler(jobs))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_get_interactions",
		Description: "List a job's running-time interactions (pending questions and their answers).",
	}, getInteractionsHandler(jobs))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_answer_interaction",
		Description: "Answer a pending interaction on a running job so the agent can continue.",
	}, answerInteractionHandler(jobs))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_get_artifacts",
		Description: "List a finished job's captured artifact files (name/size/mtime under its result dir).",
	}, getArtifactsHandler(jobs))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "bridge_get_result",
		Description: "Get a finished job's structured result.json content (E6), as a raw JSON string.",
	}, getResultHandler(jobs))

	return s
}

// Serve builds the MCP server and runs it over stdio. It blocks until stdin EOF
// or ctx cancellation. Nothing is written to stdout outside the MCP protocol
// (stdout is the transport), so callers must not print to stdout either.
func Serve(ctx context.Context, jobs *job.Service, projects *project.Registry, agents *agent.Registry) error {
	return New(jobs, projects, agents).Run(ctx, &mcp.StdioTransport{})
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
	}
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

func listProjectsHandler(projects *project.Registry) mcp.ToolHandlerFor[listProjectsInput, listProjectsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ listProjectsInput) (*mcp.CallToolResult, listProjectsOutput, error) {
		keys := projects.List() // already sorted
		out := listProjectsOutput{Projects: make([]projectEntry, 0, len(keys))}
		for _, key := range keys {
			p, err := projects.Get(key)
			if err != nil {
				continue
			}
			out.Projects = append(out.Projects, projectEntry{
				Key:               key,
				HostPath:          p.HostPath,
				ContainerPath:     p.ContainerPath,
				DefaultAgent:      p.DefaultAgent,
				AllowedAgents:     p.AllowedAgents,
				AllowedRunners:    p.AllowedRunners,
				AllowExec:         p.AllowExec,
				MaxConcurrentJobs: p.MaxConcurrentJobs,
			})
		}
		return nil, out, nil
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

func listAgentsHandler(agents *agent.Registry) mcp.ToolHandlerFor[listAgentsInput, listAgentsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ listAgentsInput) (*mcp.CallToolResult, listAgentsOutput, error) {
		list := agents.List()
		keys := make([]string, 0, len(list))
		for k := range list {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		out := listAgentsOutput{Agents: make([]agentEntry, 0, len(keys))}
		for _, k := range keys {
			ac := list[k]
			det := agents.Detect(k)
			// detail: version when available, else the captured probe error.
			detail := det.Version
			if !det.Available {
				detail = det.Error
			}
			out.Agents = append(out.Agents, agentEntry{
				Name:      k,
				Type:      ac.Type,
				Available: det.Available,
				Detail:    detail,
			})
		}
		return nil, out, nil
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
}

func runJobHandler(jobs *job.Service) mcp.ToolHandlerFor[runJobInput, jobView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in runJobInput) (*mcp.CallToolResult, jobView, error) {
		res, err := jobs.Submit(job.JobRequest{
			ProjectKey: in.ProjectKey,
			Agent:      in.Agent,
			Runner:     in.Runner,
			Prompt:     in.Prompt,
			Cmd:        in.Cmd,
			Cwd:        in.Cwd,
			TimeoutSec: in.TimeoutSec,
			Title:      in.Title,
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

func getJobHandler(jobs *job.Service) mcp.ToolHandlerFor[jobIDInput, jobView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, jobView, error) {
		res, ok := jobs.Get(in.ID)
		if !ok {
			return nil, jobView{}, fmt.Errorf("unknown job %q", in.ID)
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

func tailLogHandler(jobs *job.Service) mcp.ToolHandlerFor[tailLogInput, tailLogOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in tailLogInput) (*mcp.CallToolResult, tailLogOutput, error) {
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
		data, err := jobs.TailLog(in.ID, store.Stream(stream), maxBytes)
		if err != nil {
			return nil, tailLogOutput{}, err
		}
		return nil, tailLogOutput{Text: string(data)}, nil
	}
}

// --- bridge_cancel_job ------------------------------------------------------

func cancelJobHandler(jobs *job.Service) mcp.ToolHandlerFor[jobIDInput, jobView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, jobView, error) {
		if err := jobs.Cancel(in.ID); err != nil {
			// The only Cancel error is an unknown job id (terminal jobs are no-ops).
			return nil, jobView{}, err
		}
		res, _ := jobs.Get(in.ID)
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

func getInteractionsHandler(jobs *job.Service) mcp.ToolHandlerFor[jobIDInput, getInteractionsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, getInteractionsOutput, error) {
		list, err := jobs.GetInteractions(in.ID)
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

// --- bridge_answer_interaction ----------------------------------------------

type answerInteractionInput struct {
	ID            string `json:"id"`
	InteractionID string `json:"interaction_id"`
	Answer        string `json:"answer"`
}

func answerInteractionHandler(jobs *job.Service) mcp.ToolHandlerFor[answerInteractionInput, interactionView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in answerInteractionInput) (*mcp.CallToolResult, interactionView, error) {
		it, err := jobs.AnswerInteraction(in.ID, in.InteractionID, in.Answer)
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
func getArtifactsHandler(jobs *job.Service) mcp.ToolHandlerFor[jobIDInput, getArtifactsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, getArtifactsOutput, error) {
		// Manifest resolution (persisted ArtifactsJSON preferred, else a live scan)
		// is the shared job.Service.GetArtifactManifest — the same data-plane the
		// HTTP list endpoint uses, so the two no longer keep duplicate copies. ok=false
		// is an unknown job. Items is always non-nil.
		m, ok := jobs.GetArtifactManifest(in.ID)
		if !ok {
			return nil, getArtifactsOutput{}, fmt.Errorf("unknown job %q", in.ID)
		}
		out := getArtifactsOutput{Artifacts: make([]artifactView, 0, len(m.Items))}
		for _, it := range m.Items {
			out.Artifacts = append(out.Artifacts, artifactView{Name: it.Name, Size: it.Size, Mtime: it.Mtime})
		}
		return nil, out, nil
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
func getResultHandler(jobs *job.Service) mcp.ToolHandlerFor[jobIDInput, getResultOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in jobIDInput) (*mcp.CallToolResult, getResultOutput, error) {
		res, ok := jobs.Get(in.ID)
		if !ok {
			return nil, getResultOutput{}, fmt.Errorf("unknown job %q", in.ID)
		}
		return nil, getResultOutput{ResultJSON: res.ResultJSON}, nil
	}
}
