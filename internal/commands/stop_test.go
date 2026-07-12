package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/inhere/gofer/internal/buildinfo"
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
	for _, s := range NewWorkerCmd(buildinfo.Info{}).Subs {
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

// TestWorkerStopMissingID: no <id>, no running worker and no readable worker
// config → error (can't resolve a default).
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

// seedWorkerPid writes a worker-<id>.pid with the given pid under <config-dir>/run.
func seedWorkerPid(t *testing.T, id string, pid int) string {
	t.Helper()
	p := workerPIDFile(id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := daemon.WritePIDFile(p, pid); err != nil {
		t.Fatalf("seed pidfile %s: %v", id, err)
	}
	return p
}

// TestResolveDefaultWorkerSingleRunning: exactly one live worker pidfile → its id
// is auto-detected (no <id> needed). A stale (dead-pid) pidfile is ignored.
func TestResolveDefaultWorkerSingleRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("daemon PID liveness is not supported on Windows")
	}
	t.Setenv(config.EnvConfigDir, t.TempDir())
	seedWorkerPid(t, "solo", os.Getpid()) // alive (this test process)
	seedWorkerPid(t, "ghost", 2147483646) // dead → must be ignored
	workerStopOpts.config = ""
	defer func() { workerStopOpts.config = "" }()

	id, err := resolveDefaultWorkerID()
	if err != nil {
		t.Fatalf("resolve default worker id: %v", err)
	}
	if id != "solo" {
		t.Fatalf("auto-detected id = %q, want %q", id, "solo")
	}
}

// TestResolveDefaultWorkerMultipleRunning: more than one live worker → ambiguous,
// must error (the <id> is required).
func TestResolveDefaultWorkerMultipleRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("daemon PID liveness is not supported on Windows")
	}
	t.Setenv(config.EnvConfigDir, t.TempDir())
	seedWorkerPid(t, "alpha", os.Getpid())
	seedWorkerPid(t, "beta", os.Getpid())

	if _, err := resolveDefaultWorkerID(); err == nil {
		t.Fatal("expected ambiguous (multiple running) workers to error")
	}
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
