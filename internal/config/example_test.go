package config

import (
	"testing"

	yaml "github.com/goccy/go-yaml"

	configtmpl "github.com/inhere/gofer/config"
)

// TestExampleYAMLParses guards against config/gofer.example.yaml drifting away
// from the config structs: it decodes the embedded template, applies defaults
// and runs validate exactly like Load does, then spot-checks key field
// mappings. The commented-out callers/workers/runner_probe/retention/worker
// blocks are uncommented inline here so a typo in any of those field names
// surfaces as a decode mismatch rather than silently being ignored.
func TestExampleYAMLParses(t *testing.T) {
	if configtmpl.ExampleYAML == "" {
		t.Fatal("embedded ExampleYAML is empty")
	}

	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(configtmpl.ExampleYAML), cfg); err != nil {
		t.Fatalf("decode example: %v", err)
	}
	ApplyDefaults(cfg)
	if err := validate(cfg); err != nil {
		t.Fatalf("validate example: %v", err)
	}

	// server basics.
	if cfg.Server.Addr != "0.0.0.0:8765" {
		t.Errorf("server.addr = %q", cfg.Server.Addr)
	}
	if cfg.Server.TokenEnv != "GOFER_TOKEN" {
		t.Errorf("server.token_env = %q", cfg.Server.TokenEnv)
	}

	// storage basics.
	if cfg.Storage.DefaultExchangeSubdir != "tmp" {
		t.Errorf("storage.default_exchange_subdir = %q", cfg.Storage.DefaultExchangeSubdir)
	}
	if cfg.Storage.DefaultResultSubdir != "gofer" {
		t.Errorf("storage.default_result_subdir = %q", cfg.Storage.DefaultResultSubdir)
	}

	// projects: keys present, host_path populated, capture_diff stays unset (nil).
	p1, ok := cfg.Projects["my-project1"]
	if !ok {
		t.Fatal("project my-project1 missing")
	}
	if p1.HostPath == "" {
		t.Error("my-project1 host_path empty")
	}
	if p1.DefaultAgent != "codex" {
		t.Errorf("my-project1 default_agent = %q", p1.DefaultAgent)
	}
	if p1.MaxConcurrentJobs != 4 {
		t.Errorf("my-project1 max_concurrent_jobs = %d", p1.MaxConcurrentJobs)
	}

	// agents / runners: representative entries decode.
	if a, ok := cfg.Agents["codex"]; !ok || a.Type != "cli-agent" || a.Command != "codex" {
		t.Errorf("agents.codex = %+v ok=%v", a, ok)
	}
	if r, ok := cfg.Runners["docker-peer"]; !ok || r.Type != "peer-http" || r.BaseURL == "" {
		t.Errorf("runners.docker-peer = %+v ok=%v", r, ok)
	}
}

// TestExampleCommentedBlocksDecode verifies the commented optional sections
// (callers/workers/runner_probe/retention/worker-runner) use field names that
// actually map onto the structs. The example keeps them commented (so a fresh
// copy parses), but a drifted field name there would mislead users; this test
// decodes the documented shape directly against the structs.
func TestExampleCommentedBlocksDecode(t *testing.T) {
	const optional = `
server:
  callers:
    - id: ci
      token_env: GOFER_CALLER_CI_TOKEN
  workers:
    builder-1:
      token_env: GOFER_WORKER_BUILDER1_TOKEN
      labels: [linux, gpu]
  runner_probe:
    interval_seconds: 30
    timeout_seconds: 5
storage:
  db_path: /var/lib/gofer/gofer.db
  retention:
    max_age_days: 14
    max_count: 5000
    prune_interval_minutes: 60
runners:
  builder:
    type: worker
    worker_id: builder-1
`
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(optional), cfg); err != nil {
		t.Fatalf("decode optional blocks: %v", err)
	}

	if len(cfg.Server.Callers) != 1 || cfg.Server.Callers[0].ID != "ci" ||
		cfg.Server.Callers[0].TokenEnv != "GOFER_CALLER_CI_TOKEN" {
		t.Errorf("callers = %+v", cfg.Server.Callers)
	}
	w, ok := cfg.Server.Workers["builder-1"]
	if !ok || w.TokenEnv != "GOFER_WORKER_BUILDER1_TOKEN" || len(w.Labels) != 2 {
		t.Errorf("workers.builder-1 = %+v ok=%v", w, ok)
	}
	if cfg.Server.RunnerProbe.IntervalSeconds != 30 || cfg.Server.RunnerProbe.TimeoutSeconds != 5 {
		t.Errorf("runner_probe = %+v", cfg.Server.RunnerProbe)
	}
	if cfg.Storage.DBPath != "/var/lib/gofer/gofer.db" {
		t.Errorf("db_path = %q", cfg.Storage.DBPath)
	}
	if cfg.Storage.Retention.MaxAgeDays != 14 || cfg.Storage.Retention.MaxCount != 5000 ||
		cfg.Storage.Retention.IntervalMinutes != 60 {
		t.Errorf("retention = %+v", cfg.Storage.Retention)
	}
	r, ok := cfg.Runners["builder"]
	if !ok || r.Type != "worker" || r.WorkerID != "builder-1" {
		t.Errorf("runners.builder = %+v ok=%v", r, ok)
	}
}
