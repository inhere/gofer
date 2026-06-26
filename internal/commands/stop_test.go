package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
)

func TestStopUnknownTarget(t *testing.T) {
	c := bindCmd(NewStopCmd())
	c.Arg("target").WithValue("nope")
	err := runStop(c, nil)
	if err == nil {
		t.Fatal("expected unknown stop target to error")
	}
	assertCodedExit(t, err)
}

func TestStopWorkerMissingID(t *testing.T) {
	c := bindCmd(NewStopCmd())
	c.Arg("target").WithValue("worker")
	err := runStop(c, nil)
	if err == nil {
		t.Fatal("expected stop worker without id to error")
	}
	assertCodedExit(t, err)
}

// TestStopServeNotRunning: no pidfile → idempotent no-op (returns nil).
func TestStopServeNotRunning(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	c := bindCmd(NewStopCmd())
	c.Arg("target").WithValue("serve")
	if err := runStop(c, nil); err != nil {
		t.Fatalf("stop of a not-running serve should be a no-op, got: %v", err)
	}
}

// TestStopStaleWorkerPidfile: a pidfile pointing at a dead PID → no-op + the
// stale pidfile is cleaned.
func TestStopStaleWorkerPidfile(t *testing.T) {
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

	c := bindCmd(NewStopCmd())
	c.Arg("target").WithValue("worker")
	c.Arg("id").WithValue("ghost")
	if err := runStop(c, nil); err != nil {
		t.Fatalf("stop of a dead-pid worker should be a no-op, got: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("stale pidfile should be cleaned, stat err=%v", err)
	}
}
