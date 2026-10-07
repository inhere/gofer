package agent

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestSessionFamilyAndCompat(t *testing.T) {
	claude := config.AgentConfig{Type: TypeCLIAgent, Command: "claude"}
	claudeACP := config.AgentConfig{Type: TypeACPAgent, Command: "npx"}
	codex := config.AgentConfig{Type: TypeCLIAgent, Command: "codex"}
	codexACP := config.AgentConfig{Type: TypeACPAgent, Command: "npx"}
	custom := config.AgentConfig{Type: TypeCLIAgent, Command: "/opt/bin/claude"}

	if got := SessionFamily("my-claude", custom); got != "claude" {
		t.Fatalf("command base fallback family = %q", got)
	}
	if got := SessionFamily("codex-acp", codexACP); got != "codex" {
		t.Fatalf("codex-acp family = %q, want codex (real-host 2026-10-05: its session id is the rollout id codex resume reads)", got)
	}
	if _, ok := builtinSessionFamilies["tty-claude"]; ok {
		t.Fatal("tty-claude family entry should be gone")
	}
	cases := []struct {
		a, b   string
		ac, bc config.AgentConfig
		want   bool
	}{
		{"claude-acp", "claude", claudeACP, claude, true},
		{"claude", "claude-acp", claude, claudeACP, true},
		{"claude", "claude", claude, claude, true},
		{"claude-acp", "codex", claudeACP, codex, false},
		{"codex-acp", "codex", codexACP, codex, true},
		{"codex", "codex-acp", codex, codexACP, true},
		{"codex-acp", "claude", codexACP, claude, false},
		{"codex-acp", "claude-acp", codexACP, claudeACP, false},
		{"codex-acp", "codex-acp", codexACP, codexACP, true},
	}
	for _, c := range cases {
		if got := SessionCompatible(c.a, c.ac, c.b, c.bc); got != c.want {
			t.Errorf("SessionCompatible(%s,%s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestResumeCapabilities(t *testing.T) {
	cli := config.AgentConfig{Type: TypeCLIAgent, Command: "claude",
		SessionResume: []string{"--resume", "{{session_id}}"}, SessionResumeInteractive: []string{"--resume", "{{session_id}}"}}
	caps := ResumeCapabilities("claude", cli)
	if !caps.SessionResume || !caps.SessionResumeInteractive || caps.LoadSession || caps.Family != "claude" {
		t.Fatalf("cli caps = %+v", caps)
	}
	off := false
	acp := config.AgentConfig{Type: TypeACPAgent, ACP: &config.ACPConfig{LoadSession: &off}}
	if ResumeCapabilities("x-acp", acp).LoadSession {
		t.Fatal("acp.load_session=false must report LoadSession=false")
	}
	if !ResumeCapabilities("y-acp", config.AgentConfig{Type: TypeACPAgent}).LoadSession {
		t.Fatal("acp default must report LoadSession=true")
	}
}

func TestSessionFamilyConfigured(t *testing.T) {
	cli := config.AgentConfig{Type: TypeCLIAgent, Command: "myagent", SessionFamily: "MyFam"}
	acp := config.AgentConfig{Type: TypeACPAgent, Command: "myagent", SessionFamily: "myfam"}
	plain := config.AgentConfig{Type: TypeCLIAgent, Command: "myagent"}
	if got := SessionFamily("myagent-cli", cli); got != "myfam" {
		t.Fatalf("configured family (case-folded) = %q", got)
	}
	if !SessionCompatible("myagent-cli", cli, "myagent-acp", acp) || !SessionCompatible("myagent-acp", acp, "myagent-cli", cli) {
		t.Fatal("same configured family must be compatible both ways")
	}
	if SessionCompatible("myagent-cli", cli, "myagent-x", plain) {
		t.Fatal("an agent without a family must not join one")
	}
	// A configured family never leaks into the built-in ones: claude stays claude, and
	// a self-built agent declaring `claude` joins it on purpose.
	claude := config.AgentConfig{Type: TypeCLIAgent, Command: "claude"}
	if SessionCompatible("claude", claude, "myagent-cli", cli) {
		t.Fatal("unrelated family joined claude")
	}
	join := config.AgentConfig{Type: TypeCLIAgent, Command: "myagent", SessionFamily: "claude"}
	if !SessionCompatible("claude", claude, "myagent-cli", join) {
		t.Fatal("explicit session_family: claude should join the built-in family")
	}
	// The explicit value overrides the built-in table for a built-in key.
	override := config.AgentConfig{Type: TypeCLIAgent, Command: "claude", SessionFamily: "solo"}
	if got := SessionFamily("claude", override); got != "solo" {
		t.Fatalf("override = %q", got)
	}
	if ResumeCapabilities("myagent-cli", cli).Family != "myfam" {
		t.Fatal("ResumeCapabilities must report the configured family")
	}
}
