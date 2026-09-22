package config

import (
	"path/filepath"
	"testing"
)

// TestEffectiveRetryPolicyResolution pins the four-layer retry resolution
// (R2/AUTO-03): request > project > agent > server, the NEAREST layer that says
// anything wins and it replaces the WHOLE policy (nothing is merged field by
// field, so a layer can never leave a "half policy" behind), and every layer empty
// resolves to nil = retry off.
func TestEffectiveRetryPolicyResolution(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
server:
  token_env: MY_TOKEN
  retry:
    max_attempts: 5
    backoff_sec: [900]
agents:
  codex:
    command: codex
    retry:
      max_attempts: 2
      backoff_sec: [120]
projects:
  self:
    host_path: /tmp/self
    retry:
      max_attempts: 3
  off:
    host_path: /tmp/off
    retry:
      max_attempts: 1
  plain:
    host_path: /tmp/plain
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	requested := &RetryPolicy{MaxAttempts: 4}

	cases := []struct {
		name    string
		project string
		agent   string
		req     *RetryPolicy
		wantMax int
		want    string // which layer answered
	}{
		{"request wins over every layer", "self", "codex", requested, 4, "request"},
		{"project wins over agent+server", "self", "codex", nil, 3, "project"},
		{"agent wins over server", "plain", "codex", nil, 2, "agent"},
		{"server is the last resort", "plain", "unknown", nil, 5, "server"},
		{"unknown project/agent falls to server", "nope", "nope", nil, 5, "server"},
		{"an explicit max_attempts:1 (off) is not re-enabled by an outer layer", "off", "codex", nil, 1, "project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.EffectiveRetryPolicy(tc.project, tc.agent, tc.req)
			if got == nil {
				t.Fatalf("EffectiveRetryPolicy(%q,%q) = nil, want the %s layer", tc.project, tc.agent, tc.want)
			}
			if got.MaxAttempts != tc.wantMax {
				t.Fatalf("max_attempts = %d, want %d (the %s layer)", got.MaxAttempts, tc.wantMax, tc.want)
			}
		})
	}

	// Whole-layer replacement: the project layer does not inherit the agent's
	// backoff table — "half a policy" is exactly what this resolution avoids.
	if got := cfg.EffectiveRetryPolicy("self", "codex", nil); len(got.BackoffSec) != 0 || len(got.OnExitCodes) != 0 {
		t.Fatalf("project policy = %+v, want it taken as written (no agent/server merge)", got)
	}
	// The request layer is returned as-is (identity), so a caller can tell which
	// layer answered.
	if got := cfg.EffectiveRetryPolicy("plain", "unknown", requested); got != requested {
		t.Fatalf("requested policy = %+v, want the request pointer back", got)
	}
}

// TestEffectiveRetryPolicyAllLayersEmptyIsOff: a config that never mentions retry
// stays off (the pre-R2 behaviour) — no layer to answer means nil, not a default.
func TestEffectiveRetryPolicyAllLayersEmptyIsOff(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
server:
  token_env: MY_TOKEN
agents:
  codex:
    command: codex
projects:
  self:
    host_path: /tmp/self
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.EffectiveRetryPolicy("self", "codex", nil); got != nil {
		t.Fatalf("EffectiveRetryPolicy = %+v, want nil (retry off)", got)
	}
	var nilCfg *Config
	if got := nilCfg.EffectiveRetryPolicy("self", "codex", nil); got != nil {
		t.Fatalf("nil config = %+v, want nil", got)
	}
}
