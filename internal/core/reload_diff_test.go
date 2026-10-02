package core

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestReloadChangedIgnoresAgentRedetect(t *testing.T) {
	old := &config.Config{Agents: map[string]config.AgentConfig{"codex": {Command: "codex"}}}
	old.MarkInjectedAgents(map[string]bool{"codex": true})
	newCfg := &config.Config{}
	result := reloadResult(old, newCfg, "config.yaml")
	for _, changed := range result.Changed {
		if changed == "agents" {
			t.Fatalf("changed=%v includes agents from runtime redetection", result.Changed)
		}
	}
}
