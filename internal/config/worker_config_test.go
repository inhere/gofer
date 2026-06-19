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
    initial_ms: 500
    max_ms: 30000
    jitter: 0.2
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
	if wc.ServerLink.Reconnect.InitialMS != 500 || wc.ServerLink.Reconnect.MaxMS != 30000 || wc.ServerLink.Reconnect.Jitter != 0.2 {
		t.Fatalf("reconnect = %+v", wc.ServerLink.Reconnect)
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
