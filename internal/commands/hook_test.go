package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestResolveHookRunnerClientMode pins which execution machine a hook's session
// registration names (design §9.1 v0.5). A pure client node has no worker.yaml,
// so it must NOT claim "server" — it reports the configured runner label or
// nothing at all; an empty runner is the honest "we do not know where this
// session runs", which is what makes the server say so instead of dispatching
// into the void. Worker and server modes keep their existing labels.
func TestResolveHookRunnerClientMode(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)

	t.Setenv(config.EnvRunMode, config.RunModeClient)
	// The --runner flag (bound to GOFER_HOOK_RUNNER) names the worker that lives
	// in this container: it wins.
	if got := resolveHookRunner("w-container"); got != "w-container" {
		t.Fatalf("client mode with a runner label = %q, want w-container", got)
	}
	// Nothing configured: empty, NOT "server".
	if got := resolveHookRunner(""); got != "" {
		t.Fatalf("client mode without a runner label = %q, want empty", got)
	}

	// Worker mode: the local worker.yaml worker_id (unchanged).
	path := filepath.Join(cfgDir, config.WorkerConfigFileName)
	if err := os.WriteFile(path, []byte("worker_id: w-local\n"), 0o644); err != nil {
		t.Fatalf("write worker.yaml: %v", err)
	}
	t.Setenv(config.EnvRunMode, config.RunModeWorker)
	if got := resolveHookRunner(""); got != "w-local" {
		t.Fatalf("worker mode = %q, want w-local", got)
	}

	// Server mode: "server" (unchanged).
	t.Setenv(config.EnvRunMode, config.RunModeServer)
	if got := resolveHookRunner(""); got != "server" {
		t.Fatalf("server mode = %q, want server", got)
	}
}
