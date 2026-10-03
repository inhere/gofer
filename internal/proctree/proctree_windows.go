//go:build windows

package proctree

import (
	"errors"
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive is STILL_ACTIVE (STATUS_PENDING), the exit code GetExitCodeProcess
// reports for a process that has not exited. x/sys/windows does not export it.
const stillActive = 259

// Tree contains one process and its descendants through a job object.
type Tree struct {
	job windows.Handle
}

const jobLimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK

// New returns an empty containment. The job object itself is created in Attach, right
// after the process starts (see the race note there).
func New() *Tree { return &Tree{} }

// Configure is a no-op on Windows: the containment is a job object, and nothing on
// exec.Cmd can join one before the process exists.
func (t *Tree) Configure(*exec.Cmd) {}

// Attach creates the job object and assigns the started process to it. A failure is
// reported (and leaves the Tree inert) rather than fatal: containment is a
// protection, not a precondition for running.
//
// Race: the process exists for the microseconds between Start and this call, so a
// descendant it spawns in that window is not a job member. Closing it would need
// CREATE_SUSPENDED + ResumeThread, which os/exec does not expose; the window is orders
// of magnitude smaller than any launcher's first spawn.
func (t *Tree) Attach(cmd *exec.Cmd) error {
	if t == nil || cmd == nil || cmd.Process == nil {
		return errors.New("proctree: attach without a started process")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("proctree: create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			// KILL_ON_JOB_CLOSE keeps ordinary descendants contained. BREAKAWAY_OK
			// permits only a child that explicitly asks for CREATE_BREAKAWAY_FROM_JOB
			// (the daemon path) to leave this job object.
			LimitFlags: jobLimitFlags,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("proctree: set job limits: %w", err)
	}
	// PROCESS_SET_QUOTA + PROCESS_TERMINATE is what AssignProcessToJobObject requires.
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("proctree: open process %d: %w", cmd.Process.Pid, err)
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("proctree: assign process %d to job: %w", cmd.Process.Pid, err)
	}
	t.job = job
	return nil
}

// Kill terminates every process in the tree. Idempotent.
func (t *Tree) Kill() {
	if t == nil || t.job == 0 {
		return
	}
	_ = windows.TerminateJobObject(t.job, 1)
}

// Release closes the job handle. KILL_ON_JOB_CLOSE makes that the containment's final
// act: a descendant that outlived the direct child is killed here too, so nothing a
// job process spawned outlives the job's use of it. The Tree is inert afterwards.
func (t *Tree) Release() {
	if t == nil || t.job == 0 {
		return
	}
	_ = windows.CloseHandle(t.job)
	t.job = 0
}

// Alive reports whether a live process with pid exists. ERROR_ACCESS_DENIED means it
// exists but belongs to another user / a higher integrity level, so it is alive; every
// other open error (notably ERROR_INVALID_PARAMETER, "no such pid") means it is not. A
// process that has exited keeps its pid until its handle is closed, hence the exit-code
// check rather than a successful open.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
