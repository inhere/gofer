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
