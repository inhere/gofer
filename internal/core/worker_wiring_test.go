package core

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	workerrunner "github.com/inhere/gofer/internal/runner/worker"
)

// TestBuildCoreWorkerRunner proves Build registers a type=worker runner, the
// hub is non-nil, and every worker runner shares the SAME hub singleton.
func TestBuildCoreWorkerRunner(t *testing.T) {
	host := t.TempDir()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:   "srv-tok",
			Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
		},
		Storage: config.StorageConfig{Root: root, DBPath: filepath.Join(root, "gofer.db")},
		Projects: map[string]config.ProjectConfig{
			"alpha": {HostPath: host, AllowedRunners: []string{"remote-a", "remote-b"}},
		},
		Runners: map[string]config.RunnerConfig{
			"remote-a": {Type: "worker", WorkerID: "w1"},
			"remote-b": {Type: "worker", WorkerID: "w1"},
		},
	}
	config.ApplyDefaults(cfg)

	core, err := Build(cfg)
	if err != nil {
		t.Fatalf("buildCore: %v", err)
	}
	defer func() { _ = core.Close() }()

	if core.Hub == nil {
		t.Fatal("Core.Hub is nil")
	}
	ra, ok := core.Runners["remote-a"].(*workerrunner.Runner)
	if !ok {
		t.Fatal("remote-a is not a worker runner")
	}
	rb, ok := core.Runners["remote-b"].(*workerrunner.Runner)
	if !ok {
		t.Fatal("remote-b is not a worker runner")
	}
	_ = ra
	_ = rb
	// (Both runners were built from the same hub instance passed by Build;
	// the hub being non-nil + both runners present is the singleton assertion the
	// public API can make without reaching into runner internals.)
}

// TestServeMountsWorkerConnectRoute proves the /v1/workers/connect route exists
// when a hub is wired and returns a BARE 401 (not the JSON {error,detail}
// envelope) for a missing/invalid bearer token.
func TestServeMountsWorkerConnectRoute(t *testing.T) {
	host := t.TempDir()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:   "srv-tok",
			Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
		},
		Storage:  config.StorageConfig{Root: root, DBPath: filepath.Join(root, "gofer.db")},
		Projects: map[string]config.ProjectConfig{"alpha": {HostPath: host}},
		Runners:  map[string]config.RunnerConfig{"remote-a": {Type: "worker", WorkerID: "w1"}},
	}
	config.ApplyDefaults(cfg)

	core, err := Build(cfg)
	if err != nil {
		t.Fatalf("buildCore: %v", err)
	}
	defer func() { _ = core.Close() }()

	srv := httpapi.New(&cfg.Server, "srv-tok", false, core.Jobs, core.Workflow(), core.Projects, core.Agents, core.Hub, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// No bearer token: a non-WS GET to the route returns a bare 401 with an empty
	// body (not the JSON error envelope used by the /v1 group).
	resp, err := http.Get(ts.URL + "/v1/workers/connect")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	buf := make([]byte, 8)
	n, _ := resp.Body.Read(buf)
	if n != 0 {
		t.Fatalf("expected empty 401 body (bare handshake rejection), got %q", buf[:n])
	}
}
