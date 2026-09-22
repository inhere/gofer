// Package daemon turns a foreground gofer process (serve / worker) into a
// detached background process. Go cannot fork() safely (the runtime owns
// multiple threads), so the "-d" path re-execs the SAME binary with an
// env sentinel (EnvSentinel) set: the parent spawns the detached child and
// exits; the child sees the sentinel, skips daemonization and runs the real
// command. A pidfile records the child PID for duplicate-start detection and for
// the `serve stop` / `worker stop` subcommands. The platform-specific detach /
// signal mechanics live in daemon_unix.go and daemon_windows.go.
package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvSentinel marks that the current process is already the detached child, so
// re-exec is not attempted again (would otherwise fork-bomb). The parent sets it
// to "1" on the child's environment; Daemonized() reads it.
const EnvSentinel = "GOFER_DAEMONIZED"

// ErrAlreadyRunning is returned by Spawn when the pidfile points at a live
// process — a second `-d` start is refused rather than spawning a duplicate.
var ErrAlreadyRunning = errors.New("already running")

// Options describes one daemonization: a diagnostic name plus the runtime file
// locations (resolved by the caller, typically config.RuntimeFilePath).
type Options struct {
	Name    string // diagnostic label, e.g. "serve" / "worker-<id>"
	PIDPath string // pidfile absolute path
	LogPath string // child stdout/stderr sidecar output target (for example *.out.log)
}

// Session reports which OS session a process runs in and whether that is a session a
// user can reach. On Windows two separate questions are answered, because they are
// NOT the same one (W2 measured it on an RDP-only host): Interactive means "this
// process sits on a user desktop" — any session but the service session 0, RDP
// sessions included, which is what decides whether GUI automation can work at all;
// Console narrows that to the session attached to the PHYSICAL console, which an RDP
// login does not use (there interactive=true, console=false is normal, because the
// console session is the idle one). On unix it is the zero value, there being no
// equivalent concept.
type Session struct {
	ID          uint32
	Interactive bool
	Console     bool
}

// SessionInfo returns the session of the CURRENT process. It is implemented per
// platform (daemon_unix.go / daemon_windows.go).

// Daemonized reports whether the current process is the detached child (the
// env sentinel is set). The command layer calls this to decide whether to
// re-exec (parent) or run the real command (child).
func Daemonized() bool { return os.Getenv(EnvSentinel) == "1" }

// Spawn re-execs the current binary as a detached background child and returns
// its PID. It refuses to start when the pidfile already points at a live process
// (ErrAlreadyRunning). A stale pidfile (dead PID) is silently overwritten. Only
// the PARENT calls Spawn; the child (Daemonized()==true) never reaches here.
func Spawn(o Options) (int, error) {
	if pid, err := ReadPIDFile(o.PIDPath); err == nil && PIDAlive(pid) {
		return 0, fmt.Errorf("%s: %w (pid=%d, pidfile=%s)", o.Name, ErrAlreadyRunning, pid, o.PIDPath)
	}

	// Ensure the runtime dirs exist before the child opens its log / we write
	// the pidfile (config-dir/run is created on first daemon start).
	if err := os.MkdirAll(filepath.Dir(o.LogPath), 0o755); err != nil {
		return 0, fmt.Errorf("create log dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(o.PIDPath), 0o755); err != nil {
		return 0, fmt.Errorf("create pid dir: %w", err)
	}

	cmd, err := reexecDetached(o.LogPath)
	if err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	// Reap the child when it eventually exits. A caller that outlives it (a test,
	// a long-lived invocation) would otherwise keep an unreaped child: on unix that
	// child is a zombie — kill(pid,0) still succeeds, so PIDAlive would keep claiming
	// the daemon is running long after it stopped. The goroutine dies with the parent
	// (which for `serve -d` exits immediately; the child is then adopted by init),
	// so this only ever matters for callers that stay alive.
	go func() { _ = cmd.Wait() }()
	if err := WritePIDFile(o.PIDPath, pid); err != nil {
		// The child is already detached and running; surface the pidfile failure
		// but do not kill it — losing the pidfile only breaks `stop`, not the run.
		return pid, fmt.Errorf("child started (pid=%d) but writing pidfile failed: %w", pid, err)
	}
	return pid, nil
}

// Claim records the current process in pidPath so `serve stop` / `worker stop`
// and the worker scan (runningWorkerIDs) can find it, whether it was started
// detached (-d) or in the foreground (a supervisor / a terminal).
//
// A pidfile already held by ANOTHER live process is left untouched: owned is
// false and the returned release is a no-op, so a second instance (a different
// config dir, or a stale-looking pidfile) never steals or deletes the record.
// The caller only warns in that case — a pidfile clash must not refuse startup
// (a real conflict shows up as a bind error).
//
// release re-reads the file and removes it only while it still holds our pid, so
// a successor that already claimed the pidfile is not deleted by our shutdown.
func Claim(pidPath string) (release func(), owned bool) {
	noop := func() {}
	if pid, err := ReadPIDFile(pidPath); err == nil && pid != os.Getpid() && PIDAlive(pid) {
		return noop, false
	}
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o755); err != nil {
		slog.Warn("daemon.pidfile_claim_failed", "pidfile", pidPath, "error", err)
		return noop, false
	}
	if err := WritePIDFile(pidPath, os.Getpid()); err != nil {
		slog.Warn("daemon.pidfile_claim_failed", "pidfile", pidPath, "error", err)
		return noop, false
	}
	self := os.Getpid()
	return func() {
		if pid, err := ReadPIDFile(pidPath); err == nil && pid == self {
			RemovePIDFile(pidPath)
		}
	}, true
}

// WritePIDFile writes pid to path atomically (write temp + rename) so a reader
// never observes a half-written file.
func WritePIDFile(path string, pid int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadPIDFile reads and parses the PID stored in path. A missing file or
// non-numeric content is an error (callers treat any error as "not running").
func ReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pidfile %s: %w", path, err)
	}
	return pid, nil
}

// RemovePIDFile deletes the pidfile, ignoring a missing file. The detached child
// calls this on graceful shutdown so a stale pidfile never lingers.
func RemovePIDFile(path string) {
	_ = os.Remove(path)
}
