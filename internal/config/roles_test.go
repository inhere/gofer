package config

import (
	"path/filepath"
	"testing"
)

// TestLoadRolesSection verifies the E35 roles: section decodes into Config.Roles
// (agent/system_prompt/project/tags) and that ApplyDefaults leaves a nil roles map
// as an initialised empty map (no panic on lookup).
func TestLoadRolesSection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
projects:
  demo:
    host_path: /tmp/demo
roles:
  reviewer:
    agent: claude
    system_prompt: "You are a strict code reviewer"
    project: demo
    tags: [review, ci]
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rc, ok := cfg.Roles["reviewer"]
	if !ok {
		t.Fatal("missing role reviewer")
	}
	if rc.Agent != "claude" {
		t.Errorf("role agent = %q, want claude", rc.Agent)
	}
	if rc.SystemPrompt != "You are a strict code reviewer" {
		t.Errorf("role system_prompt = %q", rc.SystemPrompt)
	}
	if rc.Project != "demo" {
		t.Errorf("role project = %q, want demo", rc.Project)
	}
	if len(rc.Tags) != 2 || rc.Tags[0] != "review" {
		t.Errorf("role tags = %v", rc.Tags)
	}
}

// TestLoadSupervisorSection verifies the E25 supervisor: section decodes, and that
// a config without it leaves Supervisor nil (answerer off — conservative default).
func TestLoadSupervisorSection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
projects:
  demo:
    host_path: /tmp/demo
supervisor:
  enabled: true
  interval_sec: 3
  auto_answer: true
  escalate_to: "role:supervisor"
  max_rounds_per_job: 5
  allow_prompt_regex: ["staging"]
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Supervisor == nil {
		t.Fatal("supervisor section not decoded")
	}
	sc := cfg.Supervisor
	if !sc.Enabled || !sc.AutoAnswer || sc.IntervalSec != 3 || sc.MaxRoundsPerJob != 5 {
		t.Fatalf("supervisor decoded wrong: %+v", sc)
	}
	if sc.EscalateTo != "role:supervisor" || len(sc.AllowPromptRegex) != 1 || sc.AllowPromptRegex[0] != "staging" {
		t.Fatalf("supervisor fields wrong: %+v", sc)
	}
}

// TestNoSupervisorSectionLeavesNil verifies a config with no supervisor: keeps
// Supervisor nil (answerer disabled).
func TestNoSupervisorSectionLeavesNil(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, "projects:\n  demo:\n    host_path: /tmp/demo\n")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Supervisor != nil {
		t.Fatalf("expected nil Supervisor, got %+v", cfg.Supervisor)
	}
}

// TestApplyDefaultsInitsRolesMap verifies a config with no roles: gets a non-nil
// empty Roles map after defaults (so a lookup is a clean miss, not a nil panic).
func TestApplyDefaultsInitsRolesMap(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if cfg.Roles == nil {
		t.Fatal("ApplyDefaults left Roles nil")
	}
	if _, ok := cfg.Roles["nope"]; ok {
		t.Fatal("unexpected role in empty map")
	}
}

// TestValidateRejectsBadSupervisorRegex: an uncompilable allow_prompt_regex fails
// config validation (fail-fast) instead of being silently skipped at supervisor
// construction (6ct0.4).
func TestValidateRejectsBadSupervisorRegex(t *testing.T) {
	cfg := &Config{Supervisor: &SupervisorConfig{
		Enabled:          true,
		AllowPromptRegex: []string{"ok.*", "(unclosed"},
	}}
	if err := validate(cfg); err == nil {
		t.Fatal("expected validate error for invalid allow_prompt_regex")
	}
	cfg.Supervisor.AllowPromptRegex = []string{"ok.*", "^DEPLOY$"}
	if err := validate(cfg); err != nil {
		t.Fatalf("valid regexes should pass: %v", err)
	}
}

// TestValidateSupervisorReconciler: the P4b reconciler needs a roles.supervisor preset
// WITH a project (the reconciler submits with no -p) when desired_supervisors>0; a
// negative count is rejected. Both gaps were caught by E2E ("unknown project"/spawn fail).
func TestValidateSupervisorReconciler(t *testing.T) {
	// desired<0 rejected.
	cfg := &Config{Supervisor: &SupervisorConfig{DesiredSupervisors: -1}}
	if err := validate(cfg); err == nil {
		t.Fatal("expected error for negative desired_supervisors")
	}
	// desired>0 with no roles.supervisor preset rejected.
	cfg = &Config{Supervisor: &SupervisorConfig{DesiredSupervisors: 1}}
	if err := validate(cfg); err == nil {
		t.Fatal("expected error: desired_supervisors>0 without roles.supervisor")
	}
	// preset present but no project rejected (reconciler can't resolve a project).
	cfg = &Config{
		Supervisor: &SupervisorConfig{DesiredSupervisors: 1},
		Roles:      map[string]RoleConfig{"supervisor": {Agent: "codex"}},
	}
	if err := validate(cfg); err == nil {
		t.Fatal("expected error: roles.supervisor without project")
	}
	// preset with project passes.
	cfg.Roles = map[string]RoleConfig{"supervisor": {Agent: "codex", Project: "p1"}}
	if err := validate(cfg); err != nil {
		t.Fatalf("valid reconciler config should pass: %v", err)
	}
	// desired=0 (default) needs nothing.
	if err := validate(&Config{Supervisor: &SupervisorConfig{}}); err != nil {
		t.Fatalf("desired_supervisors=0 should pass: %v", err)
	}
}

// TestValidateRejectsNegativePresenceTTL: negative presence seconds are a config
// mistake (0 = use default) and are rejected (6ct0.2/.4).
func TestValidateRejectsNegativePresenceTTL(t *testing.T) {
	cfg := &Config{Presence: PresenceConfig{TTLSec: -1}}
	if err := validate(cfg); err == nil {
		t.Fatal("expected validate error for negative presence ttl_sec")
	}
	cfg.Presence = PresenceConfig{TTLSec: 30, MessageTTLSec: 3600, PruneIntervalSec: 0}
	if err := validate(cfg); err != nil {
		t.Fatalf("non-negative presence config should pass: %v", err)
	}
}
