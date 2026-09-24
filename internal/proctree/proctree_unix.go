//go:build unix

package proctree

import (
	"errors"
	"os/exec"
	"syscall"
)

// Tree contains one process and its descendants through a process group.
type Tree struct {
	pid int
}

// New returns an empty containment; Attach records the started process's pid.
func New() *Tree { return &Tree{} }

// Configure makes the process its own process-group leader BEFORE it starts, so every
// descendant it spawns is in that group (children inherit the group unless they change
// it) and one signal can reach all of them.
func (t *Tree) Configure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// Attach records the started process's pid — with Configure's Setpgid, that pid IS the
// group id.
func (t *Tree) Attach(cmd *exec.Cmd) error {
	if t == nil || cmd == nil || cmd.Process == nil {
		return errors.New("proctree: attach without a started process")
	}
	t.pid = cmd.Process.Pid
	return nil
}

// Kill sends SIGKILL to the whole process group. Idempotent: an already-empty group
// yields ESRCH, which is ignored.
func (t *Tree) Kill() {
	if t == nil || t.pid <= 0 {
		return
	}
	_ = syscall.Kill(-t.pid, syscall.SIGKILL)
}

// Release is a no-op on unix: unlike a Windows job handle, a process group has nothing
// to close, and signalling a group whose leader has already been REAPED is not safe
// (the leader's pid could have been recycled and would then be signalled as an
// unrelated group). Terminating survivors is therefore Kill's explicit job, and a
// caller that wants them gone calls it (Windows gets that cleanup for free through
// KILL_ON_JOB_CLOSE).
func (t *Tree) Release() {}

// Alive reports whether a process with pid exists. Signal 0 performs error checking
// without delivering a signal: nil → alive; EPERM → exists but owned by another user
// (still alive); ESRCH → no such process. A zombie counts as alive (its pid is not
// reusable yet), which is what a test wants when it asks "has it been reaped/killed".
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
