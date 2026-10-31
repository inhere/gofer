package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The `mail.agents` block (设计 2026-10-22 mail-agent-address「D2」): which names can be addressed,
// their defaults, "0 = 不限" for the two rotation numbers, and what is refused at load.

func loadMailCfg(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, body)
	cfg, _, err := Load(p)
	return cfg, err
}

const mailAgentBase = "projects:\n  demo:\n    host_path: /tmp/demo\n"

func TestMailAgentsDefaults(t *testing.T) {
	cfg, err := loadMailCfg(t, mailAgentBase+"mail:\n  agents:\n    suga:\n      agent: suga-acp\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Mail.EffectiveAgentsMaxResident(); got != DefaultMailAgentsMaxResident {
		t.Fatalf("agents_max_resident default = %d", got)
	}
	a, ok := cfg.Mail.Agent("SUGA")
	if !ok {
		t.Fatal("the name must match case-insensitively")
	}
	if a.Identity() != "suga-acp" {
		t.Fatalf("agent = %q", a.Identity())
	}
	if got := a.EffectiveIdle(); got != 120*time.Minute {
		t.Fatalf("idle default = %v", got)
	}
	if got := a.EffectiveMaxLetters(); got != 50 {
		t.Fatalf("max_letters default = %d", got)
	}
	if got := a.EffectiveMaxHours(); got != 24 {
		t.Fatalf("max_hours default = %d", got)
	}
	if names := cfg.Mail.AgentNames(); len(names) != 1 || names[0] != "suga" {
		t.Fatalf("names = %v", names)
	}
}

func TestMailAgentsOverrides(t *testing.T) {
	cfg, err := loadMailCfg(t, mailAgentBase+
		"mail:\n  agents_max_resident: 2\n  agents:\n    codex:\n      agent: codex-acp\n      args: [--model, m1]\n      runner: w-1\n"+
		"      idle_minutes: 30\n      max_letters: 0\n      max_hours: 0\n      prompt: 用中文\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Mail.EffectiveAgentsMaxResident(); got != 2 {
		t.Fatalf("agents_max_resident = %d", got)
	}
	a, ok := cfg.Mail.Agent("codex")
	if !ok {
		t.Fatal("codex missing")
	}
	if a.EffectiveIdle() != 30*time.Minute || a.Runner != "w-1" || a.Prompt != "用中文" || len(a.Args) != 2 {
		t.Fatalf("agent = %+v", a)
	}
	// An explicit 0 turns that rotation axis off; it is NOT the default.
	if got := a.EffectiveMaxLetters(); got != 0 {
		t.Fatalf("max_letters: 0 must mean unlimited, got %d", got)
	}
	if got := a.EffectiveMaxHours(); got != 0 {
		t.Fatalf("max_hours: 0 must mean unlimited, got %d", got)
	}
}

func TestMailAgentsApproval(t *testing.T) {
	cfg, err := loadMailCfg(t, mailAgentBase+
		"mail:\n  agents:\n    suga:\n      agent: suga-acp\n      approval: {mode: ask, timeout_sec: 60}\n"+
		"    quiet:\n      agent: codex-acp\n")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := cfg.Mail.Agent("suga")
	if !ok || a.Approval == nil {
		t.Fatalf("suga approval = %+v, want the configured block", a.Approval)
	}
	if a.Approval.Mode != ApprovalAsk || a.Approval.TimeoutSec != 60 {
		t.Fatalf("approval = %+v, want mode=ask timeout=60", *a.Approval)
	}
	// Unset stays nil: the mailbox then uses the project policy (unchanged behaviour).
	if quiet, _ := cfg.Mail.Agent("quiet"); quiet.Approval != nil {
		t.Fatalf("quiet approval = %+v, want nil", quiet.Approval)
	}
}

func TestMailAgentsValidation(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"no agent", "  agents:\n    suga:\n      idle_minutes: 5\n", "mail.agents.suga.agent"},
		{"negative idle", "  agents:\n    suga:\n      agent: suga-acp\n      idle_minutes: -1\n", "mail.agents.suga.idle_minutes"},
		{"negative letters", "  agents:\n    suga:\n      agent: suga-acp\n      max_letters: -2\n", "mail.agents.suga.max_letters"},
		{"negative hours", "  agents:\n    suga:\n      agent: suga-acp\n      max_hours: -2\n", "mail.agents.suga.max_hours"},
		{"negative cap", "  agents_max_resident: -1\n", "mail.agents_max_resident"},
		{"at in the name", "  agents:\n    \"su@ga\":\n      agent: suga-acp\n", "cannot contain"},
		{"unknown approval mode", "  agents:\n    suga:\n      agent: suga-acp\n      approval: {mode: on}\n", "mail.agents.suga: unknown approval.mode"},
		{"unknown approval kind", "  agents:\n    suga:\n      agent: suga-acp\n      approval:\n        mode: ask\n        auto_allow_kinds: [write]\n",
			"mail.agents.suga: unknown approval.auto_allow_kinds"},
		{"negative approval timeout", "  agents:\n    suga:\n      agent: suga-acp\n      approval: {timeout_sec: -1}\n", "mail.agents.suga: approval.timeout_sec"},
	} {
		_, err := loadMailCfg(t, mailAgentBase+"mail:\n"+tc.body)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// mail.agents.<名字>.agent must name an acp-agent: refused at load, not at the first start.
func TestMailAgentsAgentMustBeACP(t *testing.T) {
	agents := func(typ string) string {
		return "agents:\n  worker-x:\n    type: " + typ + "\n    command: echo\n"
	}
	body := mailAgentBase + "mail:\n  agents:\n    suga:\n      agent: worker-x\n"
	_, err := loadMailCfg(t, agents("cli-agent")+body)
	if err == nil || !strings.Contains(err.Error(), "mail.agents.suga.agent") || !strings.Contains(err.Error(), "acp-agent") {
		t.Fatalf("cli-agent: err=%v, want it to name mail.agents.suga.agent and acp-agent", err)
	}
	if _, err := loadMailCfg(t, agents("acp-agent")+body); err != nil {
		t.Fatalf("acp-agent must load: %v", err)
	}
}
