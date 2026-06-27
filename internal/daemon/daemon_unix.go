//go:build unix

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// reexecDetached starts a copy of the current binary fully detached from the
// controlling terminal: a new session (Setsid) so it survives the parent's exit
// and is not in the parent's process group, with stdin closed and stdout/stderr
// redirected to the log file. The EnvSentinel guard makes the child skip its own
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
