package job

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// MCP-06 T1: the binding union is decided at submit and kept on the row; an unknown
// name, or an agent that cannot take MCP servers, is a rejected submit.
func TestMCPBindingResolvedAndPersisted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, _ := newSkillService(t, root, func(cfg *config.Config) {
		cfg.MCPServers = map[string]config.MCPServerConfig{
			"docs": {Command: "docs-mcp"},
			"db":   {Command: "db-mcp"},
		}
		a := cfg.Agents["ok"]
		a.MCPFormat = config.MCPFormatClaudeJSON
		a.MCP = []string{"docs"}
		cfg.Agents["ok"] = a
		p := cfg.Projects["self"]
		p.AllowedAgents = append(p.AllowedAgents, "plain")
		cfg.Projects["self"] = p
		cfg.Agents["plain"] = config.AgentConfig{Type: "cli-agent", Command: cfg.Agents["ok"].Command, Args: []string{"{{prompt}}"}}
	})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30,
		MCP: []string{"db"},
	})
	if !slices.Equal(final.MCP, []string{"docs", "db"}) {
		t.Fatalf("result MCP = %v, want [docs db]", final.MCP)
	}
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob: ok=%v err=%v", ok, err)
	}
	if got := fromRecord(rec).MCP; !slices.Equal(got, []string{"docs", "db"}) {
		t.Fatalf("row MCP = %v", got)
	}
	if got := fromRecord(jobstore.JobRecord{}).MCP; got != nil {
		t.Fatalf("empty row MCP = %v", got)
	}
	if !strings.Contains(final.RequestJSON, `"mcp":["docs","db"]`) {
		t.Fatalf("request_json lacks the decided list: %s", final.RequestJSON)
	}

	for name, req := range map[string]JobRequest{
		"unknown name":     {ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", MCP: []string{"nope"}},
		"agent w/o format": {ProjectKey: "self", Agent: "plain", Runner: "local", Cwd: ".", Prompt: "x", MCP: []string{"db"}},
	} {
		if _, err := s.Submit(req); err == nil || !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: err=%v, want ErrInvalidRequest", name, err)
		}
	}
	// --no-mcp turns the agent's own binding off too.
	off := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30, NoMCP: true})
	if len(off.MCP) != 0 {
		t.Fatalf("--no-mcp result MCP = %v", off.MCP)
	}
}
