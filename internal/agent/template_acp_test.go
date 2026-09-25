package agent

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestClaudeACPTemplateHasReadOnlyMode: claude-code-acp 的 ACP 模式是
// default / acceptEdits / plan / bypassPermissions，只读对应 plan。模板不带 acp.modes
// 映射时 `job run -a claude-acp --read-only` 会被准入直接拒掉（"has no read-only mode"）——
// ACP-02 真机验收发现的 F12 ③。
func TestClaudeACPTemplateHasReadOnlyMode(t *testing.T) {
	cfg := &config.Config{}
	Resolve(cfg, newFake("claude-acp"))

	ac, ok := cfg.Agents["claude-acp"]
	if !ok {
		t.Fatal("Resolve reported claude-acp available but injected no agent")
	}
	if ac.ACP == nil {
		t.Fatal("the claude-acp template carries no acp block")
	}
	if got := ac.ACP.Modes["read_only"]; got != "plan" {
		t.Fatalf("claude-acp acp.modes.read_only = %q, want %q", got, "plan")
	}
}

// TestClaudeACPTemplateEnablesSettingsEnv: F14 (2026-09-25) — claude-code-acp resolves
// credentials through the Claude Agent SDK before the session starts, so it reads
// neither gofer's environment nor claude's own `~/.claude/settings.json` env block
// (real machine: `-32000 Authentication required`). The template therefore turns the
// inheritance ON by default, which lets the key live in exactly one place.
//
// The other ACP templates (and any agent the operator declares without the field)
// inherit nothing: this is an opt-in for the adapter that needs it, not a new global
// behaviour.
func TestClaudeACPTemplateEnablesSettingsEnv(t *testing.T) {
	cfg := &config.Config{}
	Resolve(cfg, newFake("claude-acp", "omp-acp", "codex-acp"))

	if ac, ok := cfg.Agents["claude-acp"]; !ok || ac.ACP == nil {
		t.Fatalf("claude-acp agent = %+v, want an injected template with an acp block", ac)
	} else if ac.ACP.ClaudeSettingsEnv == nil || !*ac.ACP.ClaudeSettingsEnv {
		t.Fatalf("claude-acp acp.claude_settings_env = %v, want explicit true", ac.ACP.ClaudeSettingsEnv)
	}
	for _, key := range []string{"omp-acp", "codex-acp"} {
		if cfg.Agents[key].ACP.InheritsClaudeSettingsEnv() {
			t.Errorf("%s inherits claude's settings env by default, want the switch off", key)
		}
	}

	// 显式 false 可关 — and a declared agent wins WHOLE over the template, so the
	// operator's own definition (field false, or absent entirely) is what runs.
	off := false
	cfg = &config.Config{Agents: map[string]config.AgentConfig{
		"claude-acp": {Type: TypeACPAgent, Command: "npx", ACP: &config.ACPConfig{ClaudeSettingsEnv: &off}},
	}}
	Resolve(cfg, newFake("claude-acp"))
	if got := cfg.Agents["claude-acp"]; got.ACP.InheritsClaudeSettingsEnv() {
		t.Fatalf("an explicit claude_settings_env: false was overridden by the template: %+v", got.ACP)
	}
}
