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

// TestReloadCeilingOnlyChangeIsHot: changing only a caller's ceiling is applied by the reload,
// so the report lists server as changed and does NOT ask for a restart; changing anything
// else of a caller (its token) still does.
func TestReloadCeilingOnlyChangeIsHot(t *testing.T) {
	no := false
	base := func(projects []string, token string) *config.Config {
		return &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{
			{ID: "ci", Token: token, Ceiling: &config.CallerCeiling{Projects: projects, ReadOnly: &no}},
		}}}
	}
	result := reloadResult(base([]string{"app-*"}, "t1"), base([]string{"app-a"}, "t1"), "config.yaml")
	if len(result.RestartRequired) != 0 {
		t.Fatalf("ceiling-only change asks for a restart: %v", result.RestartRequired)
	}
	found := false
	for _, c := range result.Changed {
		found = found || c == "server"
	}
	if !found {
		t.Fatalf("changed=%v, want server", result.Changed)
	}
	result = reloadResult(base([]string{"app-a"}, "t1"), base([]string{"app-a"}, "t2"), "config.yaml")
	if len(result.RestartRequired) == 0 {
		t.Fatal("a token change must still ask for a restart")
	}
}
