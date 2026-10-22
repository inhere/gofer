package mcpserver

import (
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/mcptools"
)

// TestEveryRegisteredToolDeclaresAnnotations is the guard against a new tool slipping
// in unclassified: every tool of every surface (operator, project-scoped, leader,
// steward) must carry MCP annotations, and its readOnlyHint must agree with
// internal/mcptools — the registry the approval gate auto-allows from. A tool with no
// registry entry already panics at registration (annotateGoferTool); this test names
// the missing ones instead of crashing a build, and catches an annotation written by
// hand that drifted from the registry.
func TestEveryRegisteredToolDeclaresAnnotations(t *testing.T) {
	surfaces := []struct {
		name string
		env  func(t *testing.T)
		srv  func(t *testing.T) *mcp.Server
	}{
		{name: "operator", srv: func(t *testing.T) *mcp.Server {
			jobs, projects, agents := testCore(t)
			return NewLocal(jobs, projects, agents)
		}},
		{name: "project-scoped", srv: func(t *testing.T) *mcp.Server {
			jobs, projects, agents := testCore(t)
			return newServer(newLocalBackend(jobs, projects, agents), "self")
		}},
		{name: "leader", env: func(t *testing.T) {
			t.Setenv(envLeaderPlan, "plan-1")
			t.Setenv(envJobID, "job-leader")
		}, srv: func(t *testing.T) *mcp.Server {
			jobs, projects, agents := testCore(t)
			return NewLocal(jobs, projects, agents)
		}},
		{name: "steward", env: func(t *testing.T) {
			t.Setenv(envSteward, "1")
			t.Setenv(envJobID, "steward-job")
		}, srv: func(t *testing.T) *mcp.Server {
			jobs, projects, agents := testCore(t)
			return NewLocal(jobs, projects, agents)
		}},
	}
	// Every registered gofer tool must be in the registry: collect the union across
	// surfaces, so a tool that only the operator surface has is still checked.
	seen := map[string]bool{}
	for _, surface := range surfaces {
		t.Run(surface.name, func(t *testing.T) {
			if surface.env != nil {
				surface.env(t)
			}
			session := connectTo(t, surface.srv(t))
			res, err := session.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			if len(res.Tools) == 0 {
				t.Fatal("surface exposes no tools")
			}
			for _, tl := range res.Tools {
				seen[tl.Name] = true
				entry, ok := mcptools.Lookup(tl.Name)
				if !ok {
					t.Errorf("%s: tool %q is not classified in internal/mcptools", surface.name, tl.Name)
					continue
				}
				if tl.Annotations == nil {
					t.Errorf("%s: tool %q declares no annotations (readOnlyHint missing)", surface.name, tl.Name)
					continue
				}
				wantReadOnly := entry.Class == mcptools.ReadOnly
				if tl.Annotations.ReadOnlyHint != wantReadOnly {
					t.Errorf("%s: tool %q readOnlyHint = %v, registry class = %s",
						surface.name, tl.Name, tl.Annotations.ReadOnlyHint, entry.Class)
				}
				if tl.Annotations.DestructiveHint == nil {
					t.Errorf("%s: tool %q leaves destructiveHint implicit", surface.name, tl.Name)
					continue
				}
				if wantDestructive := entry.Class == mcptools.Destructive; *tl.Annotations.DestructiveHint != wantDestructive {
					t.Errorf("%s: tool %q destructiveHint = %v, registry class = %s",
						surface.name, tl.Name, *tl.Annotations.DestructiveHint, entry.Class)
				}
				if wantIdem := wantReadOnly || entry.Idempotent; tl.Annotations.IdempotentHint != wantIdem {
					t.Errorf("%s: tool %q idempotentHint = %v, want %v", surface.name, tl.Name, tl.Annotations.IdempotentHint, wantIdem)
				}
			}
		})
	}
	// The registry must describe gofer's surface, not more: an entry no surface
	// registers is a stale classification (it would auto-allow a tool that does not
	// exist — harmless — but it hides that the tool was renamed or dropped).
	var stale []string
	for _, entry := range mcptools.All() {
		if !seen[entry.Name] {
			stale = append(stale, entry.Name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("internal/mcptools classifies tools no surface registers: %v", stale)
	}
}
