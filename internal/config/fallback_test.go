package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// fallbackAgentYAML is a minimal but complete config whose only business is the
// fallback keys: a codex agent carrying an agent-level candidate list, a project
// that overrides it, and the two server blocks. agentBlock / projectBlock are
// spliced in verbatim (already indented) so one helper covers every shape.
func fallbackAgentYAML(t *testing.T, agentBlock, projectBlock, serverBlock string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, `
projects:
  demo:
    host_path: /tmp/demo
    allowed_agents: [codex, omp, claude, exec]
`+projectBlock+`
agents:
  codex:
    type: cli-agent
    command: codex
    args: ["{{prompt}}"]
    fallback_agents: [omp]
  omp:
    type: cli-agent
    command: omp
    args: ["{{prompt}}"]
  claude:
    type: cli-agent
    command: claude
    args: ["{{prompt}}"]
`+agentBlock+serverBlock)
	return p
}

// TestFallbackAgentsResolveOrderAndValidation pins the SUP-01 P3 configuration
// surface: the candidate resolution order (job > project > agent), the two server
// switches and their defaults (on_failure on, pre_dispatch off), the health
// thresholds with their defaults, and the load-time validation that keeps a
// candidate an agent the framework can actually run in batch mode.
func TestFallbackAgentsResolveOrderAndValidation(t *testing.T) {
	cfg := &Config{
		Agents: map[string]AgentConfig{
			"codex": {Type: "cli-agent", Command: "codex", FallbackAgents: []string{"omp"}},
			"omp":   {Type: "cli-agent", Command: "omp"},
		},
		Projects: map[string]ProjectConfig{
			// demo overrides the agent-level list; plain inherits it.
			"demo":  {AllowedAgents: []string{"codex", "omp", "claude"}, AgentFallbacks: map[string][]string{"codex": {"claude", "omp"}}},
			"plain": {},
		},
	}

	t.Run("project overrides agent level", func(t *testing.T) {
		got := cfg.AgentFallbacksFor("demo", "codex")
		if strings.Join(got, ",") != "claude,omp" {
			t.Fatalf("AgentFallbacksFor(demo, codex) = %v, want the project override [claude omp]", got)
		}
	})
	t.Run("agent level is the default", func(t *testing.T) {
		got := cfg.AgentFallbacksFor("plain", "codex")
		if strings.Join(got, ",") != "omp" {
			t.Fatalf("AgentFallbacksFor(plain, codex) = %v, want the agent-level [omp]", got)
		}
	})
	t.Run("no config means no candidates", func(t *testing.T) {
		if got := cfg.AgentFallbacksFor("plain", "omp"); len(got) != 0 {
			t.Fatalf("an agent without fallback_agents must resolve to none, got %v", got)
		}
		if got := cfg.AgentFallbacksFor("ghost", "ghost"); len(got) != 0 {
			t.Fatalf("an unknown project must resolve to none, got %v", got)
		}
	})

	t.Run("server switch defaults", func(t *testing.T) {
		// Absent block: transfer after a failure is ON (decision 1), pre-dispatch is OFF
		// ("我明明指定了 codex" must not be silently rewritten by default).
		if !cfg.AgentFallbackOnFailure() {
			t.Fatal("agent_fallback.on_failure must default to true")
		}
		if cfg.AgentFallbackPreDispatch() {
			t.Fatal("agent_fallback.pre_dispatch must default to false")
		}
		off := false
		cfg.Server.AgentFallback = &AgentFallbackConfig{OnFailure: &off, PreDispatch: true}
		if cfg.AgentFallbackOnFailure() {
			t.Fatal("an explicit on_failure:false must disable the transfer")
		}
		if !cfg.AgentFallbackPreDispatch() {
			t.Fatal("an explicit pre_dispatch:true must be honoured")
		}
		cfg.Server.AgentFallback = nil
	})

	t.Run("health defaults and overrides", func(t *testing.T) {
		h := cfg.EffectiveAgentHealth()
		if h.WindowSec != DefaultAgentHealthWindowSec || h.DegradedAfter != DefaultAgentDegradedAfter || h.RecoverAfterOK != DefaultAgentRecoverAfterOK {
			t.Fatalf("health defaults = %+v, want window %d / degraded %d / recover %d",
				h, DefaultAgentHealthWindowSec, DefaultAgentDegradedAfter, DefaultAgentRecoverAfterOK)
		}
		cfg.Server.AgentHealth = &AgentHealthConfig{WindowSec: 600, DegradedAfter: 5, RecoverAfterOK: 2}
		h = cfg.EffectiveAgentHealth()
		if h.WindowSec != 600 || h.DegradedAfter != 5 || h.RecoverAfterOK != 2 {
			t.Fatalf("explicit agent_health = %+v, want it carried verbatim", h)
		}
		cfg.Server.AgentHealth = nil
	})

	t.Run("yaml wiring", func(t *testing.T) {
		path := fallbackAgentYAML(t,
			"",
			"    agent_fallbacks:\n      codex: [omp, claude]\n",
			"server:\n  agent_fallback:\n    on_failure: false\n    pre_dispatch: true\n  agent_health:\n    window_sec: 900\n    degraded_after: 2\n    recover_after_ok: 3\n")
		loaded, _, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := loaded.Projects["demo"].AgentFallbacks["codex"]; strings.Join(got, ",") != "omp,claude" {
			t.Fatalf("project agent_fallbacks = %v, want [omp claude]", got)
		}
		if got := loaded.Agents["codex"].FallbackAgents; strings.Join(got, ",") != "omp" {
			t.Fatalf("agent fallback_agents = %v, want [omp]", got)
		}
		if loaded.AgentFallbackOnFailure() || !loaded.AgentFallbackPreDispatch() {
			t.Fatalf("server.agent_fallback did not load: on_failure=%v pre_dispatch=%v",
				loaded.AgentFallbackOnFailure(), loaded.AgentFallbackPreDispatch())
		}
		if h := loaded.EffectiveAgentHealth(); h.WindowSec != 900 || h.DegradedAfter != 2 || h.RecoverAfterOK != 3 {
			t.Fatalf("server.agent_health did not load: %+v", h)
		}
	})

	// Load-time validation: a candidate the framework cannot run as a batch job is a
	// config error, not a silent skip at fallback time (the job that would have
	// fallen back is exactly the one that cannot afford a surprise).
	t.Run("rejects unknown candidate", func(t *testing.T) {
		path := fallbackAgentYAML(t, "", "", "")
		write(t, path, strings.Replace(read(t, path), "fallback_agents: [omp]", "fallback_agents: [ghost]", 1))
		_, _, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Fatalf("an undeclared fallback candidate must fail the load, got %v", err)
		}
	})
	t.Run("rejects exec candidate", func(t *testing.T) {
		path := fallbackAgentYAML(t, "", "", "")
		write(t, path, strings.Replace(read(t, path), "fallback_agents: [omp]", "fallback_agents: [exec]", 1))
		_, _, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "exec") {
			t.Fatalf("an exec fallback candidate must fail the load, got %v", err)
		}
	})
	t.Run("rejects interactive-only candidate", func(t *testing.T) {
		path := fallbackAgentYAML(t, "  tty:\n    type: cli-agent\n    command: tty\n    interactive: true\n", "", "")
		write(t, path, strings.Replace(read(t, path), "fallback_agents: [omp]", "fallback_agents: [tty]", 1))
		_, _, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "tty") {
			t.Fatalf("an interactive-only fallback candidate must fail the load, got %v", err)
		}
	})
	t.Run("rejects unknown candidate in a project override", func(t *testing.T) {
		path := fallbackAgentYAML(t, "", "    agent_fallbacks:\n      codex: [ghost]\n", "")
		_, _, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Fatalf("an undeclared project-level candidate must fail the load, got %v", err)
		}
	})
	t.Run("rejects a negative health threshold", func(t *testing.T) {
		path := fallbackAgentYAML(t, "", "", "server:\n  agent_health:\n    degraded_after: -1\n")
		_, _, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "degraded_after") {
			t.Fatalf("a negative degraded_after must fail the load, got %v", err)
		}
	})
}
