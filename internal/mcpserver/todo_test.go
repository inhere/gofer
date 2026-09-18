package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestRunJobTodoParam: gofer_run_job accepts a todo_id and it reaches the job
// request + the response (SUP-01 C: the hub resolves it into a plan and writes
// the outcome back into the checklist), and the tool's input schema declares the
// parameter so an MCP client can discover it.
func TestRunJobTodoParam(t *testing.T) {
	session, jobs := connect(t)
	if err := jobs.Meta().InsertPlan(jobstore.Plan{PlanID: "plan-mcp", Title: "plan-mcp"}); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	if err := jobs.Meta().InsertTodo(jobstore.PlanTodo{TodoID: "todo-mcp", PlanID: "plan-mcp", Title: "todo-mcp"}); err != nil {
		t.Fatalf("insert todo: %v", err)
	}

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         []string{"go", "version"},
			"cwd":         ".",
			"timeout_sec": 30,
			"todo_id":     "todo-mcp",
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.TodoID != "todo-mcp" {
		t.Fatalf("run_job response = %+v, want todo_id todo-mcp", created)
	}
	final, ok := jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &req); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if req.TodoID != "todo-mcp" || req.PlanID != "plan-mcp" {
		t.Fatalf("todo_id/plan_id did not round-trip through MCP: %+v", req)
	}

	schema := runJobSchema(t, session)
	if _, ok := schema.Properties["todo_id"]; !ok {
		t.Fatalf("input schema missing todo_id; properties=%v", schema.Properties)
	}
}

// TestUpdateTodoAppendNoteParam: gofer_update_todo accepts append_note and the
// line lands newline-joined on the note while the status stays untouched — the
// MCP twin of `plan set-todo --append-note`, which is how a job's outcome is
// written onto a checklist item.
func TestUpdateTodoAppendNoteParam(t *testing.T) {
	session, jobs := connect(t)
	if err := jobs.Meta().InsertPlan(jobstore.Plan{PlanID: "plan-ap", Title: "plan-ap"}); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	if err := jobs.Meta().InsertTodo(jobstore.PlanTodo{
		TodoID: "todo-ap", PlanID: "plan-ap", Title: "carried", Note: "first line",
	}); err != nil {
		t.Fatalf("insert todo: %v", err)
	}

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_update_todo",
		Arguments: map[string]any{
			"todo_id":     "todo-ap",
			"append_note": "job-1 ✓ no commits",
		},
	})
	if err != nil {
		t.Fatalf("CallTool update_todo: %v", err)
	}
	var tv todoView
	structured(t, res, &tv)
	if tv.Note != "first line\njob-1 ✓ no commits" {
		t.Fatalf("note = %q, want the appended line", tv.Note)
	}
	if tv.Status == "done" || tv.Done {
		t.Fatalf("appending must not complete the todo: %+v", tv)
	}

	schema := updateTodoSchema(t, session)
	if _, ok := schema.Properties["append_note"]; !ok {
		t.Fatalf("input schema missing append_note; properties=%v", schema.Properties)
	}
}

// updateTodoSchema returns gofer_update_todo's input-schema properties.
func updateTodoSchema(t *testing.T, session *mcp.ClientSession) struct {
	Properties map[string]any `json:"properties"`
} {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "gofer_update_todo" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		var out struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal schema: %v", err)
		}
		return out
	}
	t.Fatal("gofer_update_todo tool not registered")
	return struct {
		Properties map[string]any `json:"properties"`
	}{}
}
