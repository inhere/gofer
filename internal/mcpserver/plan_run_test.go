package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestPlanRunTool (PLAN-03): `gofer_plan_run` is the MCP entry to a plan's dependency
// chain — it queues the root item (and only the root), answers with the plan header, and
// the chain then walks itself as each item finishes.
func TestPlanRunTool(t *testing.T) {
	jobs, projects, agents, pres := dispatchCore(t)
	session := connectTo(t, NewLocal(jobs, projects, agents, pres))

	if err := jobs.Meta().InsertPlan(jobstore.Plan{
		PlanID: "plan-mcp-run", Title: "chain", Status: jobstore.PlanOpen,
		ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	for _, todo := range []jobstore.PlanTodo{
		{TodoID: "todo-r1", Title: "root", Status: jobstore.TodoPending, Assignee: "reader", Auto: true, Sort: 10, CreatedAt: 1, UpdatedAt: 1},
		{TodoID: "todo-r2", Title: "leaf", Status: jobstore.TodoPending, Assignee: "reader", Auto: true, Sort: 20, After: []string{"todo-r1"}, CreatedAt: 1, UpdatedAt: 1},
	} {
		todo.PlanID = "plan-mcp-run"
		if err := jobs.Meta().InsertTodo(todo); err != nil {
			t.Fatalf("insert todo %s: %v", todo.TodoID, err)
		}
	}

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_plan_run",
		Arguments: map[string]any{"plan_id": "plan-mcp-run"},
	})
	if err != nil {
		t.Fatalf("CallTool plan_run: %v", err)
	}
	var pv planView
	structured(t, res, &pv)
	if pv.PlanID != "plan-mcp-run" || pv.Status != jobstore.PlanOpen {
		t.Fatalf("plan_run response = %+v, want the plan header (open)", pv)
	}
	if pv.Paused || pv.BlockedTodo != "" {
		t.Fatalf("plan_run response = %+v, want it unpaused and unblocked", pv)
	}

	// The chain reaches its end without another call.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok, _ := jobs.Meta().GetPlan("plan-mcp-run"); ok && p.Status == jobstore.PlanDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	p, _, err := jobs.Meta().GetPlan("plan-mcp-run")
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if p.Status != jobstore.PlanDone {
		t.Fatalf("plan status = %q, want done", p.Status)
	}
	for _, id := range []string{"todo-r1", "todo-r2"} {
		td, _, _ := jobs.Meta().GetTodo(id)
		if td.Status != jobstore.TodoDone {
			t.Fatalf("%s status = %q, want done", id, td.Status)
		}
	}

	// An unknown plan is an error, not a silent success.
	miss, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_plan_run",
		Arguments: map[string]any{"plan_id": "plan-nope"},
	})
	if err != nil {
		t.Fatalf("CallTool plan_run (unknown): %v", err)
	}
	if !miss.IsError {
		t.Fatal("plan_run on an unknown plan must be an error result")
	}
}
