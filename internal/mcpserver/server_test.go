package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/job"
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
	jobs := job.NewService(cfg, projects, agents, runners)
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
		"bridge_list_projects": false,
		"bridge_list_agents":   false,
		"bridge_run_job":       false,
		"bridge_get_job":       false,
		"bridge_tail_log":      false,
		"bridge_cancel_job":    false,
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
