package mcpserver

import (
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// countingDetector reports `ghost` available and counts the detect passes it runs.
type countingDetector struct{ calls int }

func (d *countingDetector) Detect(agents map[string]config.AgentConfig) map[string]agent.DetectResult {
	d.calls++
	out := make(map[string]agent.DetectResult, len(agents))
	for key := range agents {
		switch key {
		case "ghost":
			out[key] = agent.DetectResult{Available: true, Version: "9.9.9"}
		case "exec":
			out[key] = agent.DetectResult{Available: true, Version: "builtin"}
		}
	}
	return out
}

// TestLocalBackendListAgentsServesCachedAvailability: the MCP ListAgents tool reads the
// availability cache instead of probing per call. It is the worst amplifier of the two
// availability readers — a driver agent calls it repeatedly, and it used to spawn one
// `--version` child process per agent EVERY time.
//
// Two proofs, as on the HTTP path: the detect pass count stays at the one assembly ran,
// and `ghost` (not on PATH, so unavailable to any live probe) reads back available with
// its cached version as the detail.
func TestLocalBackendListAgentsServesCachedAvailability(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"ghost": {Type: agent.TypeCLIAgent, Command: "gofer-ghost-cli-not-installed", Args: []string{"{{prompt}}"}},
	}}
	// Resolve + seed the registry exactly as core.Build does (one detect pass).
	det := &countingDetector{}
	cfg, detected := agent.Resolve(cfg, det)
	b := &localBackend{agents: agent.NewRegistryWith(cfg, detected)}

	for i := range 3 {
		got, err := b.ListAgents()
		if err != nil {
			t.Fatalf("call %d: ListAgents: %v", i, err)
		}
		entries := map[string]agentEntry{}
		for _, e := range got {
			entries[e.Name] = e
		}
		if e := entries["ghost"]; !e.Available || e.Detail != "9.9.9" {
			t.Fatalf("call %d: ghost = %+v, want the cached {available true, detail 9.9.9}", i, e)
		}
		if e := entries["exec"]; !e.Available {
			t.Fatalf("call %d: built-in exec must stay available, got %+v", i, e)
		}
	}
	if det.calls != 1 {
		t.Fatalf("3 ListAgents calls ran %d detect passes, want the 1 from assembly", det.calls)
	}
}
