package commands

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
	var haveList bool
	for _, s := range NewWorkerCmd(buildinfo.Info{}).Subs {
		if s.Name == "list" {
			haveList = true
		}
	}
	if !haveList {
		t.Fatal("missing `worker list` subcommand")
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

// TestStopDaemonHintIsPlatformNeutral: the stop path must never tell the operator
// to run a unix-only command. "SIGTERM" must not appear at all, and the manual
// hard-kill wording can only come from daemon.KillHint(pid) — taskkill /PID N /F
// on Windows, kill -9 N on unix.
func TestStopDaemonHintIsPlatformNeutral(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	pidPath := filepath.Join(dir, "run", "serve.pid")
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	c := bindCmd(NewServeStopCmd())
	selfHint := daemon.KillHint(os.Getpid())
	assertNeutral := func(step, text string) {
		t.Helper()
		if strings.Contains(text, "SIGTERM") {
			t.Fatalf("%s: wording must be platform neutral, got: %s", step, text)
		}
		if strings.Contains(text, "kill -9") && !strings.Contains(text, selfHint) {
			t.Fatalf("%s: the only hard-kill wording allowed is daemon.KillHint(pid), got: %s", step, text)
		}
	}

	// (a) pidfile pointing at a process that has already exited → not running.
	if err := daemon.WritePIDFile(pidPath, 2147483646); err != nil {
		t.Fatalf("seed dead pidfile: %v", err)
	}
	out := captureOutput(t, func() {
		if err := stopDaemon(c, pidPath, "serve"); err != nil {
			t.Fatalf("stop of a dead-pid serve should be a no-op, got: %v", err)
		}
	})
	assertNeutral("dead pidfile", out)
	if !strings.Contains(out, "未在运行") {
		t.Fatalf("a dead pidfile must be reported as not running, got: %s", out)
	}

	// (b) pidfile pointing at THIS live process. On unix Terminate signals us (the
	// registered channel swallows it so the test process survives and the graceful
	// wait times out); on Windows there is no stop event for this pid, so Terminate
	// fails outright. Both paths must stay platform neutral.
	if err := daemon.WritePIDFile(pidPath, os.Getpid()); err != nil {
		t.Fatalf("seed self pidfile: %v", err)
	}
	swallow := make(chan os.Signal, 1)
	signal.Notify(swallow, syscall.SIGTERM)
	defer signal.Stop(swallow)

	var err error
	out = captureOutput(t, func() { err = stopDaemon(c, pidPath, "serve") })
	text := out
	if err != nil {
		text += "\n" + err.Error()
	}
	assertNeutral("live self pidfile", text)
	if !strings.Contains(text, selfHint) && !strings.Contains(text, "已停止") {
		t.Fatalf("a stop that did not report success must point at daemon.KillHint(%d), got: %s", os.Getpid(), text)
	}
}
