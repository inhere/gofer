//go:build unix

package daemon

import (
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
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Env = append(os.Environ(), EnvSentinel+"=1")
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

// KillHint is the manual command an operator can run when a graceful stop timed
// out (printed by stopDaemon).
func KillHint(pid int) string { return fmt.Sprintf("kill -9 %d", pid) }

// SessionInfo has no unix equivalent: a unix process runs in whatever environment
// its parent gave it, so the zero value (Interactive=false) is the honest answer.
func SessionInfo() Session { return Session{} }
