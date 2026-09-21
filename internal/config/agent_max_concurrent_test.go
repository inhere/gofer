package config

import (
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// TestAgentMaxConcurrentParsed: `agents.<key>.max_concurrent`（JOB-11 per-agent 并发）
// 必须真的从 YAML 解出来——它按 agent 限流，解不出来就会静默变成"不限流"。
func TestAgentMaxConcurrentParsed(t *testing.T) {
	src := `
agents:
  omp:
    type: cli-agent
    command: omp
    max_concurrent: 2
  codex:
    type: cli-agent
    command: codex
`
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(src), cfg); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if got := cfg.Agents["omp"].MaxConcurrent; got != 2 {
		t.Errorf("agents.omp.max_concurrent = %d, want 2", got)
	}
	// An agent without the key is UNLIMITED (0), not some inherited default: a config
	// that never mentions it must keep the pre-JOB-11 behaviour.
	if got := cfg.Agents["codex"].MaxConcurrent; got != 0 {
		t.Errorf("agents.codex.max_concurrent = %d, want 0 (unlimited)", got)
	}
}
