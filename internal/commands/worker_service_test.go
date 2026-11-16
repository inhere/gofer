//go:build windows || linux

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestPurgeWorkerAssetsScope: --purge deletes exactly worker.yaml, the
// GOFER_WORKER_TOKEN .env line and run/worker-<id>.*, and nothing else in the
// config directory.
func TestPurgeWorkerAssetsScope(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"worker.yaml":                     "worker_id: w1\n",
		"config.yaml":                     "server: {}\n",
		".env":                            "GOFER_WORKER_TOKEN=secret\nOTHER=1\nexport GOFER_WORKER_TOKEN=secret2\n",
		filepath.Join("run", "worker-w1.pid"):  "1234\n",
		filepath.Join("run", "worker-w1.log"):  "log\n",
		filepath.Join("run", "worker-other.pid"): "5678\n",
		filepath.Join("run", "serve.pid"):      "999\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := purgeWorkerAssets(dir, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 4 {
		t.Fatalf("purged %d entries: %v", len(removed), removed)
	}
	for _, gone := range []string{"worker.yaml", filepath.Join("run", "worker-w1.pid"), filepath.Join("run", "worker-w1.log")} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived the purge: %v", gone, err)
		}
	}
	for _, kept := range []string{"config.yaml", filepath.Join("run", "worker-other.pid"), filepath.Join("run", "serve.pid")} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
	}
	dotenv, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dotenv), "GOFER_WORKER_TOKEN") || !strings.Contains(string(dotenv), "OTHER=1") {
		t.Fatalf(".env after purge: %q", dotenv)
	}
}

// TestClassifyWorkerServiceReusesServeStatusScale pins the documented exit codes
// shared with `serve status`.
func TestClassifyWorkerServiceReusesServeStatusScale(t *testing.T) {
	cases := []struct {
		state workerServiceState
		want  string
		code  int
	}{
		{workerServiceState{}, serveStatusStateUnconfig, serveStatusExitUnconfig},
		{workerServiceState{Registered: true}, serveStatusStateDown, serveStatusExitDown},
		{workerServiceState{Registered: true, Active: true}, serveStatusStateDegraded, serveStatusExitDegraded},
		{workerServiceState{Registered: true, Active: true, Verified: true}, serveStatusStateOK, serveStatusExitOK},
	}
	for _, tc := range cases {
		state, code := classifyWorkerService(tc.state)
		if state != tc.want || code != tc.code {
			t.Errorf("%+v classified as %s/%d, want %s/%d", tc.state, state, code, tc.want, tc.code)
		}
	}
}

// TestWorkerServiceCommandsDispatch runs the real gcli dispatch: the subtree
// exists, and a status of an unregistered worker reports the unconfigured exit
// code instead of a generic failure.
func TestWorkerServiceCommandsDispatch(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvConfigDir, root)
	service := NewApp("test").GetCommand("worker").GetCommand("service")
	if service == nil {
		t.Fatal("worker service missing")
	}
	for _, name := range []string{"register", "start", "stop", "status", "uninstall"} {
		if service.GetCommand(name) == nil {
			t.Fatalf("worker service %s missing", name)
		}
	}
	if code := NewApp("test").Run([]string{"worker", "service", "status", "--id", "w-none"}); code != serveStatusExitUnconfig {
		t.Fatalf("status of an unregistered worker exited %d, want %d", code, serveStatusExitUnconfig)
	}
}
