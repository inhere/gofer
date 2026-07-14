package core

import (
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// TestAgentAvailabilityIsCachedFromTheOneDetectPass is P2 T5's核心 assertion: the
// availability readers (GET /v1/agents, the MCP ListAgents tool) serve the results of
// the ONE detect pass Build already ran. They used to run a live probe per agent per
// request — with ListAgents, a tool a driver agent calls over and over, that meant a
// child process per agent per call.
//
// The detector's call count is the evidence: N reads must not add a single detect.
func TestAgentAvailabilityIsCachedFromTheOneDetectPass(t *testing.T) {
	d := detectorFor("mine") // `mine` is not on PATH: only the cache can report it available
	cr, err := Build(resolveTestConfig(t), WithAgentDetector(d))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	if d.calls != 1 {
		t.Fatalf("Build ran %d detect passes, want exactly 1", d.calls)
	}

	for i := range 3 {
		got := cr.Agents.Availability()
		if !got["mine"].Available {
			t.Fatalf("read %d: mine = %+v, want the cached available=true", i, got["mine"])
		}
		if !got["exec"].Available {
			t.Fatalf("read %d: built-in exec must stay available, got %+v", i, got["exec"])
		}
	}
	if d.calls != 1 {
		t.Fatalf("3 availability reads triggered %d detect passes, want the 1 from Build (cache miss)", d.calls)
	}
}

// TestReloadWithSwapsAvailabilityCache: a reload is a new config snapshot, so it gets
// its own detect pass — and its results replace the cache in the SAME swap as the
// config. That is how a CLI installed on a long-running server becomes visible
// (SIGHUP / `gofer worker reload`), and how an uninstalled one disappears.
func TestReloadWithSwapsAvailabilityCache(t *testing.T) {
	d := detectorFor("mine")
	cr, err := Build(resolveTestConfig(t), WithAgentDetector(d))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	// The operator swaps `mine` for `other`, and `other` is what is now installed.
	d.avail = map[string]bool{"other": true}
	newCfg := &config.Config{
		Storage: config.StorageConfig{Root: t.TempDir()},
		Agents: map[string]config.AgentConfig{
			"other": {Type: agent.TypeCLIAgent, Command: "other", Args: []string{"{{prompt}}"}},
		},
	}
	config.ApplyDefaults(newCfg)
	if err := cr.ReloadWith(newCfg); err != nil {
		t.Fatalf("ReloadWith: %v", err)
	}

	if d.calls != 2 {
		t.Fatalf("detect passes = %d, want 2 (one per config snapshot)", d.calls)
	}
	got := cr.Agents.Availability()
	if _, stale := got["mine"]; stale {
		t.Fatalf("the previous config's agent is still in the cache: %+v", got)
	}
	if !got["other"].Available {
		t.Fatalf("other = %+v, want the reload's detect result (available)", got["other"])
	}
}
