package config

import (
	"testing"
	"time"
)

func TestSynthesisConfigValidation(t *testing.T) {
	cfg := &Config{Projects: map[string]ProjectConfig{"p": {}}, Agents: map[string]AgentConfig{"claude": {}}}
	if err := (MemoryConfig{Synthesis: SynthesisConfig{Runner: "worker"}}).validate(cfg); err == nil {
		t.Fatal("worker runner must be rejected")
	}
	if err := (MemoryConfig{Synthesis: SynthesisConfig{Time: "9:15"}}).validate(cfg); err == nil {
		t.Fatal("non-padded time must be rejected")
	}
	if err := (MemoryConfig{Synthesis: SynthesisConfig{Projects: []string{"missing"}}}).validate(cfg); err == nil {
		t.Fatal("missing project must be rejected")
	}
	if err := (MemoryConfig{Synthesis: SynthesisConfig{ModelArgs: []string{"--mcp", "on"}}}).validate(cfg); err == nil {
		t.Fatal("tool enabling arg must be rejected")
	}
	if err := (MemoryConfig{Synthesis: SynthesisConfig{Enabled: true, Agent: "claude", Projects: []string{"p"}}}).validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestSynthesisRetentionDuration(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		days int
		want time.Duration
	}{{0, 90 * day}, {-1, 90 * day}, {30, 30 * day}} {
		if got := (SynthesisConfig{RetainDays: tc.days}).RetentionDuration(); got != tc.want {
			t.Errorf("retain_days=%d: %v, want %v", tc.days, got, tc.want)
		}
	}
}
