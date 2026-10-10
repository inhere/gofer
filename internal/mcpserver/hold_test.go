package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
)

// TestHoldOverMCP (gofer-9b1b): gofer_run_job takes hold / hold_reason /
// hold_timeout_sec and the view carries the hold; gofer_reject_job refuses a held job
// (a person decides it) and points at gofer_cancel_job, which withdraws it; there is no
// approve tool at all.
func TestHoldOverMCP(t *testing.T) {
	jobs, projects, agents, pres := reviewCore(t)
	session := connectTo(t, NewLocal(jobs, projects, agents, pres))
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "codex", "runner": "local", "prompt": "push it",
			"hold": true, "hold_reason": "push the release branch", "hold_timeout_sec": 600,
		},
	})
	if err != nil {
		t.Fatalf("CallTool run: %v", err)
	}
	var view jobView
	structured(t, res, &view)
	if view.Status != job.StatusAwaitingApproval || view.Hold == nil ||
		view.Hold.Reason != "push the release branch" || view.Hold.TimeoutSec != 600 || view.Hold.ExpiresAt == 0 {
		t.Fatalf("held run view = %+v hold=%+v", view, view.Hold)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "gofer_reject_job",
		Arguments: map[string]any{"job_id": view.ID, "note": "never mind"},
	})
	if err != nil {
		t.Fatalf("CallTool reject: %v", err)
	}
	if !res.IsError || !strings.Contains(toolText(res), "gofer_cancel_job") {
		t.Fatalf("rejecting a held job over MCP must be a tool error naming gofer_cancel_job: %+v", res.Content)
	}
	if cur, _ := jobs.Get(view.ID); cur.Status != job.StatusAwaitingApproval {
		t.Fatalf("a refused MCP reject moved the job to %s", cur.Status)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gofer_cancel_job", Arguments: map[string]any{"id": view.ID}})
	if err != nil {
		t.Fatalf("CallTool cancel: %v", err)
	}
	structured(t, res, &view)
	if view.Status != job.StatusCancelled || view.Hold == nil || view.Hold.Decision != job.HoldDecisionCancelled {
		t.Fatalf("cancel of a held job = %s hold=%+v", view.Status, view.Hold)
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tl := range tools.Tools {
		if strings.Contains(tl.Name, "approve") {
			t.Fatalf("MCP must not expose an approve tool, found %q", tl.Name)
		}
	}
}

// toolText joins a tool result's text content (the error message of a tool error).
func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
