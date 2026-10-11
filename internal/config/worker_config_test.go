package config

import (
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// TestDecodeServerWorkers proves the server-side workers map and a type=worker
// runner with worker_id decode into the strongly-typed model.
func TestDecodeServerWorkers(t *testing.T) {
	src := `
server:
  token: t
  workers:
    laptop-01:
      token_env: GOFER_WORKER_LAPTOP01_TOKEN
      labels: [macos, gpu]
runners:
  remote-laptop:
    type: worker
    worker_id: laptop-01
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	w, ok := cfg.Server.Workers["laptop-01"]
	if !ok {
		t.Fatal("workers[laptop-01] missing")
	}
	if w.TokenEnv != "GOFER_WORKER_LAPTOP01_TOKEN" {
		t.Fatalf("token_env = %q", w.TokenEnv)
	}
	if len(w.Labels) != 2 || w.Labels[0] != "macos" {
		t.Fatalf("labels = %v", w.Labels)
	}
	rc := cfg.Runners["remote-laptop"]
	if rc.Type != "worker" || rc.WorkerID != "laptop-01" {
		t.Fatalf("runner = %+v", rc)
	}
}

// TestDecodeWorkerConfig proves the worker.yaml top-level structure decodes.
func TestDecodeWorkerConfig(t *testing.T) {
	src := `
worker_id: laptop-01
server_link:
  urls: [wss://hub-a.internal/v1/workers/connect, wss://hub-b.internal/]
  token_env: GOFER_WORKER_TOKEN
  reconnect:
    initial_backoff_ms: 500
    max_backoff_ms: 30000
    ping_interval_sec: 10
    read_deadline_sec: 30
projects:
  self:
    host_path: /home/me/proj
    allowed_agents: [exec]
agents:
  exec:
    type: exec
runners:
  local:
    type: local
max_concurrent: 4
labels: [macos, gpu]
storage:
  root: /tmp/gofer-worker
`
	var wc WorkerConfig
	if err := yaml.Unmarshal([]byte(src), &wc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if wc.WorkerID != "laptop-01" {
		t.Fatalf("worker_id = %q", wc.WorkerID)
	}
	if len(wc.ServerLink.URLs) != 2 || wc.ServerLink.URLs[0] != "wss://hub-a.internal/v1/workers/connect" {
		t.Fatalf("server_link.urls = %v", wc.ServerLink.URLs)
	}
	if wc.ServerLink.TokenEnv != "GOFER_WORKER_TOKEN" {
		t.Fatalf("token_env = %q", wc.ServerLink.TokenEnv)
	}
	rc := wc.ServerLink.Reconnect
	if rc.InitialBackoffMS != 500 || rc.MaxBackoffMS != 30000 || rc.PingIntervalSec != 10 || rc.ReadDeadlineSec != 30 {
		t.Fatalf("reconnect = %+v", rc)
	}
	if wc.MaxConcurrent != 4 {
		t.Fatalf("max_concurrent = %d", wc.MaxConcurrent)
	}
	if _, ok := wc.Projects["self"]; !ok {
		t.Fatal("projects[self] missing")
	}
	if wc.Storage.Root != "/tmp/gofer-worker" {
		t.Fatalf("storage.root = %q", wc.Storage.Root)
	}
}

// TestDecodeWorkerTools pins the CFG-16 `tools:` block of worker.yaml onto
// WorkerConfig.Tools (name / desc / bin, bin optional).
func TestDecodeWorkerTools(t *testing.T) {
	src := `
worker_id: w1
tools:
  - name: ffmpeg
    desc: transcoder
  - name: sim
    bin: /opt/sim/bin/sim
`
	var wc WorkerConfig
	if err := yaml.Unmarshal([]byte(src), &wc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(wc.Tools) != 2 {
		t.Fatalf("tools = %+v, want 2", wc.Tools)
	}
	if got := wc.Tools[0]; got.Name != "ffmpeg" || got.Desc != "transcoder" || got.Bin != "" {
		t.Errorf("tools[0] = %+v", got)
	}
	if got := wc.Tools[1]; got.Name != "sim" || got.Bin != "/opt/sim/bin/sim" || got.EffectiveBin() != "/opt/sim/bin/sim" {
		t.Errorf("tools[1] = %+v", got)
	}
	if wc.Tools[0].EffectiveBin() != "ffmpeg" {
		t.Errorf("EffectiveBin default = %q, want the name", wc.Tools[0].EffectiveBin())
	}
	if err := ValidateTools(wc.Tools); err != nil {
		t.Errorf("ValidateTools: %v", err)
	}
}
