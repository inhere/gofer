package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/tracker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The brief tools return the CLI text; a standalone MCP has no server client, so the
// server sections say so and a plan brief (server-only) is an error.
func TestBriefTools(t *testing.T) {
	session, _ := connect(t)
	root := t.TempDir()
	store, _, err := tracker.Init(root, "mb", true)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.CreateIssue(tracker.Issue{Title: "brief me", Type: "task", Priority: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_issue_brief", Arguments: map[string]any{"id": item.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var out briefOutput
	structured(t, res, &out)
	if out.Kind != "issue" || !strings.Contains(out.Text, "# 接手包 issue "+item.ID) || !strings.Contains(out.Text, "standalone MCP has no server client") {
		t.Fatalf("issue brief = %+v", out)
	}
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_plan_brief", Arguments: map[string]any{"id": "plan-x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("plan brief without a server should fail")
	}
}
