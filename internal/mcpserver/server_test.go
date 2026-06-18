package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/job"
	"dev-agent-bridge/internal/jobstore"
	"dev-agent-bridge/internal/project"
	"dev-agent-bridge/internal/runner"
	localrunner "dev-agent-bridge/internal/runner/local"
)

// testCore builds the registries + job.Service over a temp result root with a
// single project "self" that allows the built-in exec agent and the local
// runner. Mirrors the httpapi/job test fixtures.
func testCore(t *testing.T) (*job.Service, *project.Registry, *agent.Registry) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root, // any existing dir; cwd "." resolves here
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "agent-bridge.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	jobs := job.NewService(cfg, projects, agents, runners, meta)
	return jobs, projects, agents
}

// connect wires an in-memory client<->server session over the mcpserver. The
// returned session and jobs handle let tests drive tools and wait on jobs.
func connect(t *testing.T) (*mcp.ClientSession, *job.Service) {
	t.Helper()
	jobs, projects, agents := testCore(t)
	srv := New(jobs, projects, agents)

	ctx := context.Background()
	c2s, s2c := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, s2c, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := cli.Connect(ctx, c2s, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, jobs
}

// structured decodes the structured output of a CallToolResult into out.
func structured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned error result: %+v", res.Content)
	}
	if res.StructuredContent == nil {
		t.Fatalf("tool result has no structured content")
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
}

func TestListToolsAllPresent(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"bridge_list_projects":      false,
		"bridge_list_agents":        false,
		"bridge_run_job":            false,
		"bridge_get_job":            false,
		"bridge_tail_log":           false,
		"bridge_cancel_job":         false,
		"bridge_get_interactions":   false,
		"bridge_answer_interaction": false,
	}
	for _, tl := range res.Tools {
		if _, ok := want[tl.Name]; ok {
			want[tl.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("tool %q missing from ListTools (got %d tools)", name, len(res.Tools))
		}
	}
	// All eight bridge_* tools must be registered (no more, no fewer).
	if len(res.Tools) != len(want) {
		t.Fatalf("expected %d tools, got %d: %+v", len(want), len(res.Tools), res.Tools)
	}
}

// TestRunJobInputSchemaSnakeCase asserts the SDK-derived input schema for
// bridge_run_job uses snake_case property names (matching the HTTP API).
func TestRunJobInputSchemaSnakeCase(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var runJob *mcp.Tool
	for _, tl := range res.Tools {
		if tl.Name == "bridge_run_job" {
			runJob = tl
			break
		}
	}
	if runJob == nil {
		t.Fatalf("bridge_run_job not found")
	}
	// InputSchema arrives as a map[string]any on the client; its properties keys
	// are the field names. Assert snake_case keys are present.
	b, err := json.Marshal(runJob.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("unmarshal input schema: %v", err)
	}
	for _, key := range []string{"project_key", "timeout_sec"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("input schema missing snake_case property %q; properties=%v", key, schema.Properties)
		}
	}
}

func TestListProjectsTool(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "bridge_list_projects"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out listProjectsOutput
	structured(t, res, &out)
	if len(out.Projects) != 1 || out.Projects[0].Key != "self" {
		t.Fatalf("unexpected projects: %+v", out.Projects)
	}
	if !out.Projects[0].AllowExec {
		t.Fatalf("expected allow_exec=true for self: %+v", out.Projects[0])
	}
}

func TestListAgentsTool(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "bridge_list_agents"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out listAgentsOutput
	structured(t, res, &out)
	if len(out.Agents) == 0 {
		t.Fatalf("expected non-empty agents")
	}
	var foundExec bool
	for _, a := range out.Agents {
		if a.Name == "exec" {
			foundExec = true
			if !a.Available {
				t.Fatalf("exec agent should be available: %+v", a)
			}
		}
	}
	if !foundExec {
		t.Fatalf("exec agent missing: %+v", out.Agents)
	}
}

func TestRunJobAndGet(t *testing.T) {
	session, jobs := connect(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bridge_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         []string{"go", "version"},
			"cwd":         ".",
			"timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.ID == "" {
		t.Fatalf("run_job returned no id: %+v", created)
	}
	if created.ProjectKey != "self" || created.Agent != "exec" {
		t.Fatalf("unexpected job view: %+v", created)
	}

	// Wait for terminal state via the service, then re-query through the tool.
	final, ok := jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}

	getRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bridge_get_job",
		Arguments: map[string]any{"id": created.ID},
	})
	if err != nil {
		t.Fatalf("CallTool get_job: %v", err)
	}
	var got jobView
	structured(t, getRes, &got)
	if got.Status != job.StatusDone || got.ExitCode != 0 {
		t.Fatalf("expected done/exit 0, got status=%s exit=%d", got.Status, got.ExitCode)
	}
}

func TestRunJobRejectedUnknownProject(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bridge_run_job",
		Arguments: map[string]any{
			"project_key": "ghost",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         []string{"go", "version"},
		},
	})
	// A tool error surfaces as IsError=true (not a transport error).
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown project, got success: %+v", res.StructuredContent)
	}
}

func TestTailLogTool(t *testing.T) {
	session, jobs := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bridge_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         []string{"go", "version"},
			"cwd":         ".",
			"timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if _, ok := jobs.Wait(created.ID); !ok {
		t.Fatalf("Wait: job not found")
	}

	logRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bridge_tail_log",
		Arguments: map[string]any{"id": created.ID, "stream": "stdout"},
	})
	if err != nil {
		t.Fatalf("tail_log: %v", err)
	}
	var out tailLogOutput
	structured(t, logRes, &out)
	if out.Text == "" {
		t.Fatalf("expected non-empty stdout tail")
	}
}

func TestTailLogInvalidStream(t *testing.T) {
	session, jobs := connect(t)
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bridge_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	var created jobView
	structured(t, res, &created)
	jobs.Wait(created.ID)

	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bridge_tail_log",
		Arguments: map[string]any{"id": created.ID, "stream": "bogus"},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !bad.IsError {
		t.Fatalf("expected IsError for invalid stream")
	}
}

func TestCancelJobTool(t *testing.T) {
	session, jobs := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bridge_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"sleep", "5"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)

	// Wait until the job is actually running before cancelling.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := jobs.Get(created.ID); ok && r.Status == job.StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancelRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bridge_cancel_job",
		Arguments: map[string]any{"id": created.ID},
	})
	if err != nil {
		t.Fatalf("cancel_job: %v", err)
	}
	var cancelled jobView
	structured(t, cancelRes, &cancelled)

	final, _ := jobs.Wait(created.ID)
	if final.Status != job.StatusCancelled {
		t.Fatalf("expected cancelled, got %s", final.Status)
	}
}

func TestCancelUnknownJobTool(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bridge_cancel_job",
		Arguments: map[string]any{"id": "does-not-exist"},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown job id")
	}
}

// startRunningJob submits a long-lived exec job over the tool and polls (via the
// service handle) until it reports running, so interactions can be raised while
// the job is genuinely live. It cancels the job on cleanup.
func startRunningJob(t *testing.T, session *mcp.ClientSession, jobs *job.Service) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bridge_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"sleep", "30"}, "cwd": ".", "timeout_sec": 60,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.ID == "" {
		t.Fatalf("run_job returned no id: %+v", created)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := jobs.Get(created.ID); ok &&
			(r.Status == job.StatusRunning || r.Status == job.StatusPendingInteraction) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = jobs.Cancel(created.ID) })
	return created.ID
}

// TestInteractionToolsRoundTrip drives the two new interaction tools end to end:
// a pending interaction is raised on a live job via the service (MCP has no
// create tool), then read with bridge_get_interactions, answered with
// bridge_answer_interaction, and re-read to confirm the answered state.
func TestInteractionToolsRoundTrip(t *testing.T) {
	session, jobs := connect(t)
	jobID := startRunningJob(t, session, jobs)

	created, err := jobs.CreateInteraction(jobID, job.InteractionInput{
		Type:   job.InteractionTypeQuestion,
		Prompt: "continue?",
	})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}
	if created.Status != job.InteractionPending {
		t.Fatalf("created interaction status=%s, want pending", created.Status)
	}

	// bridge_get_interactions must surface the pending interaction.
	getRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bridge_get_interactions",
		Arguments: map[string]any{"id": jobID},
	})
	if err != nil {
		t.Fatalf("get_interactions: %v", err)
	}
	var got getInteractionsOutput
	structured(t, getRes, &got)
	if len(got.Interactions) != 1 || got.Interactions[0].ID != created.ID {
		t.Fatalf("unexpected interactions: %+v", got.Interactions)
	}
	if got.Interactions[0].Status != job.InteractionPending || got.Interactions[0].JobID != jobID {
		t.Fatalf("expected pending interaction for job %s: %+v", jobID, got.Interactions[0])
	}

	// bridge_answer_interaction answers it and returns the answered view.
	ansRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bridge_answer_interaction",
		Arguments: map[string]any{
			"id": jobID, "interaction_id": created.ID, "answer": "yes-go",
		},
	})
	if err != nil {
		t.Fatalf("answer_interaction: %v", err)
	}
	var answered interactionView
	structured(t, ansRes, &answered)
	if answered.Status != job.InteractionAnswered || answered.Answer != "yes-go" {
		t.Fatalf("unexpected answered interaction: %+v", answered)
	}

	// Re-read confirms the answered state persisted.
	getRes2, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bridge_get_interactions",
		Arguments: map[string]any{"id": jobID},
	})
	if err != nil {
		t.Fatalf("get_interactions(2): %v", err)
	}
	var got2 getInteractionsOutput
	structured(t, getRes2, &got2)
	if len(got2.Interactions) != 1 || got2.Interactions[0].Status != job.InteractionAnswered {
		t.Fatalf("expected answered after re-read, got %+v", got2.Interactions)
	}
	if got2.Interactions[0].Answer != "yes-go" {
		t.Fatalf("expected answer 'yes-go', got %+v", got2.Interactions[0])
	}
}

// TestGetInteractionsEmptyIsArray asserts an unknown job yields a non-nil empty
// array (Out.interactions == []), not null.
func TestGetInteractionsEmptyIsArray(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bridge_get_interactions",
		Arguments: map[string]any{"id": "does-not-exist"},
	})
	if err != nil {
		t.Fatalf("get_interactions: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error for unknown job: %+v", res.Content)
	}
	var out getInteractionsOutput
	structured(t, res, &out)
	if out.Interactions == nil || len(out.Interactions) != 0 {
		t.Fatalf("expected empty non-nil interactions array, got %v", out.Interactions)
	}
}

// TestAnswerInteractionSchemaSnakeCase asserts the SDK-derived input schema for
// bridge_answer_interaction uses snake_case property names (incl. interaction_id).
func TestAnswerInteractionSchemaSnakeCase(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var tool *mcp.Tool
	for _, tl := range res.Tools {
		if tl.Name == "bridge_answer_interaction" {
			tool = tl
			break
		}
	}
	if tool == nil {
		t.Fatalf("bridge_answer_interaction not found")
	}
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("unmarshal input schema: %v", err)
	}
	for _, key := range []string{"id", "interaction_id", "answer"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("input schema missing snake_case property %q; properties=%v", key, schema.Properties)
		}
	}
}

// TestAnswerInteractionUnknownJobToolError asserts answering on an unknown job
// surfaces a tool error (the service error is returned directly).
func TestAnswerInteractionUnknownJobToolError(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bridge_answer_interaction",
		Arguments: map[string]any{
			"id": "ghost", "interaction_id": "x", "answer": "a",
		},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown job")
	}
}
