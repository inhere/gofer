package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestTodoToolsContinueFrom: gofer_add_todo / gofer_update_todo accept continue_from,
// the views show it with the implied `after`, and the validation failures (self, cycle,
// cross-plan) come back as error results that name the rule.
func TestTodoToolsContinueFrom(t *testing.T) {
	session, jobs := connect(t)
	meta := jobs.Meta()
	for _, id := range []string{"plan-mc-a", "plan-mc-b"} {
		if err := meta.InsertPlan(jobstore.Plan{PlanID: id, Title: id}); err != nil {
			t.Fatalf("insert plan: %v", err)
		}
	}
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool %s: %v", name, err)
		}
		return res
	}
	errText := func(res *mcp.CallToolResult) string {
		t.Helper()
		if !res.IsError {
			t.Fatalf("expected an error result, got %+v", res.StructuredContent)
		}
		s := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				s += tc.Text
			}
		}
		return s
	}
	contains := func(s, sub string) bool { return strings.Contains(s, sub) }

	var first, second, other todoView
	structured(t, call("gofer_add_todo", map[string]any{"plan_id": "plan-mc-a", "title": "first"}), &first)
	structured(t, call("gofer_add_todo", map[string]any{"plan_id": "plan-mc-a", "title": "second", "continue_from": first.TodoID}), &second)
	structured(t, call("gofer_add_todo", map[string]any{"plan_id": "plan-mc-b", "title": "other"}), &other)
	if second.ContinueFrom != first.TodoID || len(second.After) != 1 || second.After[0] != first.TodoID {
		t.Fatalf("second = continue_from %q after %v, want %q implied after", second.ContinueFrom, second.After, first.TodoID)
	}

	if msg := errText(call("gofer_update_todo", map[string]any{"todo_id": first.TodoID, "continue_from": first.TodoID})); !contains(msg, "itself") {
		t.Errorf("self: %q", msg)
	}
	if msg := errText(call("gofer_update_todo", map[string]any{"todo_id": first.TodoID, "continue_from": second.TodoID})); !contains(msg, "cycle") {
		t.Errorf("cycle: %q", msg)
	}
	if msg := errText(call("gofer_update_todo", map[string]any{"todo_id": second.TodoID, "continue_from": other.TodoID})); !contains(msg, "same plan") {
		t.Errorf("cross-plan update: %q", msg)
	}
	if msg := errText(call("gofer_add_todo", map[string]any{"plan_id": "plan-mc-a", "title": "x", "continue_from": other.TodoID})); !contains(msg, "same plan") {
		t.Errorf("cross-plan add: %q", msg)
	}

	var cleared todoView
	structured(t, call("gofer_update_todo", map[string]any{"todo_id": second.TodoID, "continue_from": ""}), &cleared)
	if cleared.ContinueFrom != "" {
		t.Fatalf("continue_from after clear = %q", cleared.ContinueFrom)
	}
}
