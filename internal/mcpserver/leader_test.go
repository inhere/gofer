package mcpserver

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// leaderToolNames is the WHITELIST a leader's gofer MCP exposes (MCP-05 阶段 B): talk
// in the thread (comment/list), look at the plan, move an item between pending and
// ready/skipped, arm a wakeup and ask a human. Deliberately absent: run_job (the leader
// does not run work itself), the review tools (a leader never accepts/rejects),
// cancel, config and every operator tool.
var leaderToolNames = []string{
	"gofer_ask_human",
	"gofer_comment",
	"gofer_get_plan",
	"gofer_list_comments",
	"gofer_update_todo",
	"gofer_wakeup_create",
}

// toolRefusalText renders why a tool call did not succeed: "" means it DID succeed.
// The SDK surfaces a handler error either as a call error or as an IsError result whose
// content carries the message, so both shapes are read here (a refusal is only useful if
// its reason is assertable).
func toolRefusalText(t *testing.T, res *mcp.CallToolResult, err error) string {
	t.Helper()
	if err != nil {
		return err.Error()
	}
	if res == nil || !res.IsError {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestLeaderToolsWhitelist pins the leader's tool surface and its one narrowed input:
// a leader's gofer MCP is built with GOFER_LEADER_PLAN set (which the job service
// exports for the leader job it started), and it must offer exactly the whitelist —
// and gofer_update_todo must refuse any status other than ready|skipped (a leader
// cannot mark work done, let alone accept it).
func TestLeaderToolsWhitelist(t *testing.T) {
	t.Setenv(envLeaderPlan, "plan-1")
	t.Setenv(envJobID, "job-leader")

	jobs, projects, agents, pres := testCore(t)
	session := connectTo(t, NewLocal(jobs, projects, agents, pres))

	ctx := context.Background()
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	want := append([]string(nil), leaderToolNames...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("leader tools = %v, want exactly %v", got, want)
	}

	// A status outside ready|skipped is refused (and done/skipped-ish aliases too:
	// only the two the design allows). The refusal must NAME the allowed pair — a
	// "todo not found" would mean the gate is not what refused it.
	for _, status := range []string{"done", "doing", "pending", "nope"} {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "gofer_update_todo",
			Arguments: map[string]any{"todo_id": "todo-1", "status": status},
		})
		msg := toolRefusalText(t, res, err)
		if msg == "" {
			t.Fatalf("gofer_update_todo status=%q was accepted by a leader", status)
		}
		if !strings.Contains(msg, "ready") {
			t.Fatalf("refusal for %q does not name the allowed statuses: %s", status, msg)
		}
	}

	// Without the leader marker the same server is the ordinary full toolset.
	t.Setenv(envLeaderPlan, "")
	full := connectTo(t, NewLocal(jobs, projects, agents, pres))
	fullTools, err := full.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools (full): %v", err)
	}
	if len(fullTools.Tools) <= len(want) {
		t.Fatalf("the non-leader MCP exposes %d tools, want the full set (> %d)", len(fullTools.Tools), len(want))
	}
	found := false
	for _, tool := range fullTools.Tools {
		if tool.Name == "gofer_reject_job" {
			found = true
		}
	}
	if !found {
		t.Fatal("the non-leader MCP lost gofer_reject_job")
	}
}
