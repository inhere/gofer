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
