//go:build unix

package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// reexecDetached starts a copy of the current binary fully detached from the
// controlling terminal: a new session (Setsid) so it survives the parent's exit
// and is not in the parent's process group, with stdin closed and stdout/stderr
// redirected to the sidecar output file. The EnvSentinel guard makes the child skip its own
// daemonization. The parent does NOT Wait — the child keeps running after Spawn
// returns and the parent exits.
func reexecDetached(logPath string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return StartDetached(self, os.Args[1:], append(os.Environ(), EnvSentinel+"=1"), logPath)
}

// StartDetached starts exe fully detached (new session) with stdin closed and
// stdout/stderr appended to logPath, in the environment env. The caller owns the
// returned Cmd (it should Wait to reap the child if it outlives it).
func StartDetached(exe string, args, env []string, logPath string) (*exec.Cmd, error) {
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Stdin = nil // equivalent to /dev/null
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = lf.Close()
		return nil, err
	}
	// The child inherited the fd; the parent no longer needs it.
	_ = lf.Close()
	return cmd, nil
}

// StartDetachedStrict is deliberately unavailable on Unix: Setsid only leaves
// the terminal/session, not the service's systemd cgroup. Upgrade helpers must
// be launched in a separate transient unit by the Linux service backend.
func StartDetachedStrict(string, []string, []string, string) (*exec.Cmd, error) {
	return nil, errors.New("strict detach requires an independent systemd unit on Unix")
}

// PIDAlive reports whether a process with pid exists. signal 0 performs error
// checking without delivering a signal: nil → alive; EPERM → exists but owned by
// another user (still alive); ESRCH → no such process.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Terminate sends SIGTERM to pid for graceful shutdown (used by `serve stop` /
// `worker stop`).
func Terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// NotifyStop is a no-op on unix: the stop signal IS a real SIGTERM, which the
// caller already receives through its own signal.Notify, so there is no second
// delivery mechanism to install here (Windows needs one because a detached
// process there has no console and therefore no signal to receive).
func NotifyStop(chan<- os.Signal) {}

// NotifyReload is a no-op on Unix because RequestReload sends a real SIGHUP.
func NotifyReload(chan<- os.Signal) {}

// RequestReload asks a Unix gofer process to reload through SIGHUP.
func RequestReload(pid int) error { return syscall.Kill(pid, syscall.SIGHUP) }

// KillHint is the manual command an operator can run when a graceful stop timed
// out (printed by stopDaemon).
func KillHint(pid int) string { return fmt.Sprintf("kill -9 %d", pid) }

// SessionInfo has no unix equivalent: a unix process runs in whatever environment
// its parent gave it, so the zero value (Interactive=false) is the honest answer.
func SessionInfo() Session { return Session{} }

// KillDetached hard-kills a process started by StartDetached together with whatever
// it spawned: Setsid made it the leader of its own process group, so the group is
// signalled as a whole (a candidate that hung before registering may have children).
func KillDetached(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return p.Kill()
}
