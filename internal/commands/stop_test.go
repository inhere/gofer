package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
)

// TestServeStopRegistered: `serve` exposes a `stop` subcommand.
func TestServeStopRegistered(t *testing.T) {
	var have bool
	for _, s := range NewServeCmd().Subs {
		if s.Name == "stop" {
			have = true
		}
	}
	if !have {
		t.Fatal("missing `serve stop` subcommand")
	}
}

// TestWorkerStopRegistered: `worker` exposes a `stop` subcommand.
func TestWorkerStopRegistered(t *testing.T) {
	var have bool
	for _, s := range NewWorkerCmd().Subs {
		if s.Name == "stop" {
			have = true
		}
	}
	if !have {
		t.Fatal("missing `worker stop` subcommand")
	}
}

// TestServeStopNotRunning: no pidfile → idempotent no-op (returns nil).
func TestServeStopNotRunning(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	c := bindCmd(NewServeStopCmd())
	if err := runServeStop(c, nil); err != nil {
		t.Fatalf("stop of a not-running serve should be a no-op, got: %v", err)
	}
}

// TestWorkerStopMissingID: no <id> and no readable worker config → error.
func TestWorkerStopMissingID(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	c := bindCmd(NewWorkerStopCmd())
	// point --worker-config at a non-existent file so the default-id fallback fails
	workerStopOpts.config = filepath.Join(t.TempDir(), "missing.yaml")
	defer func() { workerStopOpts.config = "" }()
	err := runWorkerStop(c, nil)
	if err == nil {
		t.Fatal("expected worker stop without id/config to error")
	}
	assertCodedExit(t, err)
}

// TestWorkerStopStalePidfile: an explicit <id> with a pidfile pointing at a dead
// PID → no-op + the stale pidfile is cleaned.
func TestWorkerStopStalePidfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	pidPath := workerPIDFile("ghost")
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	// A PID well above pid_max is guaranteed dead.
	if err := daemon.WritePIDFile(pidPath, 2147483646); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}

	c := bindCmd(NewWorkerStopCmd())
	c.Arg("id").WithValue("ghost")
	if err := runWorkerStop(c, nil); err != nil {
		t.Fatalf("stop of a dead-pid worker should be a no-op, got: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("stale pidfile should be cleaned, stat err=%v", err)
	}
}
