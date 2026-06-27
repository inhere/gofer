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
	LogPath string // child stdout/stderr redirect target
}

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
	if err := WritePIDFile(o.PIDPath, pid); err != nil {
		// The child is already detached and running; surface the pidfile failure
		// but do not kill it — losing the pidfile only breaks `stop`, not the run.
		return pid, fmt.Errorf("child started (pid=%d) but writing pidfile failed: %w", pid, err)
	}
	return pid, nil
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
