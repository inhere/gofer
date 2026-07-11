package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectScoped wires an in-memory session over a server built with the given
// project scope (xu64.15). scoped=="" is operator (all tools/projects).
func connectScoped(t *testing.T, scoped string) *mcp.ClientSession {
	t.Helper()
	jobs, projects, agents, pres := testCore(t)
	b := newLocalBackend(jobs, projects, agents, pres)
	return connectTo(t, newServer(b, "", "", scoped))
}

// TestFilterProjectsByScope covers the 收窄 helper directly: operator passthrough,
// scoped keeps only the match among several, unknown scope → empty non-nil.
func TestFilterProjectsByScope(t *testing.T) {
	entries := []projectEntry{{Key: "a"}, {Key: "b"}, {Key: "c"}}

	if got := filterProjectsByScope(entries, ""); len(got) != 3 {
		t.Fatalf("operator passthrough: got %d, want 3", len(got))
	}
	got := filterProjectsByScope(entries, "b")
	if len(got) != 1 || got[0].Key != "b" {
		t.Fatalf("scoped filter: got %+v, want [b]", got)
	}
	got = filterProjectsByScope(entries, "zzz")
	if got == nil || len(got) != 0 {
		t.Fatalf("unknown scope: got %+v, want empty non-nil", got)
	}
}

// TestScopedToolRegistration asserts the operator/scoped tool-registration diff:
// operator registers create_plan+attach_job; a project scope hides exactly those
// two (operator count - 2) while keeping the 放行 tools.
func TestScopedToolRegistration(t *testing.T) {
	names := func(scoped string) map[string]bool {
		session := connectScoped(t, scoped)
		res, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("ListTools(scoped=%q): %v", scoped, err)
		}
		m := make(map[string]bool, len(res.Tools))
		for _, tl := range res.Tools {
			m[tl.Name] = true
		}
		return m
	}

	op := names("")
	if !op["gofer_create_plan"] || !op["gofer_attach_job"] {
		t.Fatalf("operator must register plan-authoring tools: %+v", op)
	}

	sc := names("self")
	if sc["gofer_create_plan"] || sc["gofer_attach_job"] {
		t.Fatalf("scoped MCP must NOT register create_plan/attach_job: %+v", sc)
	}
	if len(sc) != len(op)-2 {
		t.Fatalf("scoped tool count = %d, want operator(%d)-2", len(sc), len(op))
	}
	// 放行类 (by-id / list) tools stay registered under scope.
	for _, n := range []string{"gofer_get_job", "gofer_get_plan", "gofer_run_job", "gofer_list_projects"} {
		if !sc[n] {
			t.Fatalf("scoped MCP dropped passthrough tool %q", n)
		}
	}
}

// TestScopedListProjectsTool proves the list_projects 収窄 wiring: a scope of the
// only project returns just it; a scope matching no project returns empty (not an
// error — a mis-touch guard, not authz).
func TestScopedListProjectsTool(t *testing.T) {
	callList := func(scoped string) []projectEntry {
		session := connectScoped(t, scoped)
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_list_projects"})
		if err != nil {
			t.Fatalf("list_projects(scoped=%q) transport: %v", scoped, err)
		}
		var out listProjectsOutput
		structured(t, res, &out)
		return out.Projects
	}

	if got := callList("self"); len(got) != 1 || got[0].Key != "self" {
		t.Fatalf("scoped=self: got %+v, want [self]", got)
	}
	if got := callList("ghost"); len(got) != 0 {
		t.Fatalf("scoped=ghost (no match): got %+v, want empty", got)
	}
}

// TestScopedRunJobEnforcement proves run_job 収窄: an omitted project_key defaults
// to the scope; an explicit mismatch is rejected.
func TestScopedRunJobEnforcement(t *testing.T) {
	session := connectScoped(t, "self")

	// omitted project_key → defaults to the scoped project "self".
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("run_job(default) transport: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.ProjectKey != "self" {
		t.Fatalf("scoped run_job must default project to self, got %q", created.ProjectKey)
	}

	// explicit mismatch → rejected (can't submit into another project).
	res2, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "other", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".",
		},
	})
	if err != nil {
		t.Fatalf("run_job(mismatch) transport: %v", err)
	}
	if !res2.IsError {
		t.Fatalf("scoped run_job to another project must error, got: %+v", res2.StructuredContent)
	}
}
