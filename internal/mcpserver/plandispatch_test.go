package mcpserver

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// dispatchCore builds the in-process core the PLAN-02 tools are driven against: one
// project allowing a prompt-driven cli-agent ("reader" = the test binary echoing its
// argv), so a dispatched todo really runs a job.
func dispatchCore(t *testing.T) (*job.Service, *project.Registry, *agent.Registry, *presence.Service) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"reader"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"reader": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"argv", "{{prompt}}"}},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	jobs := job.NewService(cfg, projects, agents, runners, meta, nil)
	t.Cleanup(func() { drainJobs(t, jobs) })
	return jobs, projects, agents, presence.NewService(meta)
}

// connectDispatch wires a session over the dispatch core and seeds one plan+ todo.
func connectDispatch(t *testing.T, todo jobstore.PlanTodo) (*mcp.ClientSession, *job.Service, jobstore.PlanTodo) {
	t.Helper()
	jobs, projects, agents, pres := dispatchCore(t)
	session := connectTo(t, NewLocal(jobs, projects, agents, pres))
	if err := jobs.Meta().InsertPlan(jobstore.Plan{
		PlanID: "plan-mcp-d", Title: "MCP dispatch", Status: jobstore.PlanOpen,
		ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	todo.PlanID = "plan-mcp-d"
	if err := jobs.Meta().InsertTodo(todo); err != nil {
		t.Fatalf("insert todo: %v", err)
	}
	return session, jobs, todo
}

// TestUpdateTodoAssigneeDispatches (PLAN-02 P2): assigning an agent to a READY todo
// through MCP starts the job, exactly like the HTTP/CLI paths — "指派即派发" is a
// property of the todo write, not of one interface.
func TestUpdateTodoAssigneeDispatches(t *testing.T) {
	session, jobs, todo := connectDispatch(t, jobstore.PlanTodo{
		TodoID: "todo-mcp-a", Title: "step", Status: jobstore.TodoReady, CreatedAt: 1, UpdatedAt: 1,
	})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_update_todo",
		Arguments: map[string]any{
			"todo_id":  "todo-mcp-a",
			"assignee": "reader",
		},
	})
	if err != nil {
		t.Fatalf("CallTool update_todo: %v", err)
	}
	var tv todoView
	structured(t, res, &tv)
	if tv.Assignee != "reader" {
		t.Fatalf("update_todo response = %+v, want the assignee stored", tv)
	}
	if tv.Status != jobstore.TodoDoing || tv.JobID == "" {
		t.Fatalf("update_todo response = %+v, want it dispatched (doing + job_id)", tv)
	}

	list, err := jobs.ListJobs(job.ListOpts{Plan: todo.PlanID, Limit: 10})
	if err != nil {
		t.Fatalf("list plan jobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("dispatched jobs = %+v, want exactly one", list)
	}
	if list[0].Agent != "reader" || list[0].TodoID != todo.TodoID || list[0].Channel != "plan" {
		t.Fatalf("dispatched job = %+v, want the todo's agent and linkage", list[0])
	}
	final, ok := jobs.Wait(list[0].ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", list[0].ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("job status = %s (err=%s), want done", final.Status, final.Error)
	}

	// The schema declares the new fields, so an MCP client can discover them.
	schema := updateTodoSchema(t, session)
	for _, prop := range []string{"assignee", "template", "vars", "verify", "review", "runner", "cwd", "timeout_sec"} {
		if _, ok := schema.Properties[prop]; !ok {
			t.Fatalf("gofer_update_todo schema missing %q; properties=%v", prop, schema.Properties)
		}
	}
}

// TestDispatchTodoTool (PLAN-02 P2): `gofer_dispatch_todo` is the explicit fallback an
// agent reaches for when the automatic rule cannot fire (or when it wants to re-run an
// item) — it starts the job and returns both the todo and the job it created.
func TestDispatchTodoTool(t *testing.T) {
	session, jobs, todo := connectDispatch(t, jobstore.PlanTodo{
		TodoID: "todo-mcp-x", Title: "step", Assignee: "reader",
		Status: jobstore.TodoPending, CreatedAt: 1, UpdatedAt: 1,
	})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_dispatch_todo",
		Arguments: map[string]any{"todo_id": todo.TodoID},
	})
	if err != nil {
		t.Fatalf("CallTool dispatch_todo: %v", err)
	}
	var out todoDispatchView
	structured(t, res, &out)
	if !out.Dispatched || out.Job == nil {
		t.Fatalf("dispatch_todo output = %+v, want a job", out)
	}
	if out.Job.Agent != "reader" || out.Job.TodoID != todo.TodoID {
		t.Fatalf("dispatch_todo job = %+v, want the todo's agent", out.Job)
	}
	if out.Todo.Status != jobstore.TodoDoing {
		t.Fatalf("dispatch_todo todo status = %q, want doing", out.Todo.Status)
	}
	// The explicit path ignores the lifecycle: the item was pending, and it ran.
	if _, ok := jobs.Wait(out.Job.ID); !ok {
		t.Fatalf("dispatched job %s not found", out.Job.ID)
	}

	// The tool's input schema declares todo_id.
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name != "gofer_dispatch_todo" {
			continue
		}
		found = true
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("unmarshal schema: %v", err)
		}
		if _, ok := schema.Properties["todo_id"]; !ok {
			t.Fatalf("gofer_dispatch_todo schema missing todo_id: %v", schema.Properties)
		}
	}
	if !found {
		t.Fatal("gofer_dispatch_todo tool not registered")
	}
}
