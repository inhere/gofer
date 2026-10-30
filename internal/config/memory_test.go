package config

import "testing"

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
