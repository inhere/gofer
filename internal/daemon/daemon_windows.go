//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows has no setsid, and a process without a console receives no signals, so
// the three daemon primitives are built out of different pieces:
//
//   - detach: DETACHED_PROCESS means the child inherits NO console, so closing the
//     terminal (or Ctrl+C in it) cannot take it down — the same effect jcode users
//     observe. CREATE_BREAKAWAY_FROM_JOB additionally escapes a job object the
//     terminal host put us in (Windows Terminal and some CI runners kill the whole
//     job when the window closes). A job that forbids breakaway refuses that flag
//     with ERROR_ACCESS_DENIED, and reexecDetached then retries without it.
//   - liveness: OpenProcess + GetExitCodeProcess (PIDAlive). No image-name check:
//     a reused pid is caught by the stop path, which refuses to guess (below).
//   - stop: a NAMED EVENT, not a console control event. GenerateConsoleCtrlEvent
//     only reaches processes that share the caller's console, and a detached gofer
//     has none, so `serve stop` sets Global\gofer-stop-<pid> and the target's
//     WaitForSingleObject goroutine turns that into a SIGTERM on its own signal
//     channel (NotifyStop). The event also makes stopping SAFE: the event's
//     existence proves the target is the very process this build started (a reused
//     pid has no event), which is why Terminate never falls back to
//     TerminateProcess — a hard kill would be free to shoot an unrelated process
//     that inherited the pid. The stale pidfile is a human decision (KillHint).
//
// The event lives in the Global\ namespace so a stop issued from another session
// (e.g. a job running under the session-0 service) still reaches a serve running
// on the user's desktop. Cross-USER stops are impossible (default DACL): the
// operator gets the taskkill hint instead.
const stopEventPrefix = `Global\gofer-stop-`

// stillActive is STILL_ACTIVE (winnt.h) — the exit code GetExitCodeProcess reports
// while a process is still running. x/sys/windows does not export it.
const stillActive = 259

// noConsoleSession is what WTSGetActiveConsoleSessionId returns when no session is
// attached to the console.
const noConsoleSession = 0xFFFFFFFF

// detachFlags are on every attempt; CREATE_BREAKAWAY_FROM_JOB is added on the
// first attempt only (see reexecDetached).
const detachFlags = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP

func stopEventName(pid int) string { return fmt.Sprintf("%s%d", stopEventPrefix, pid) }

// reexecDetached starts a copy of the current binary detached from our console,
// with stdin closed and stdout/stderr appended to the sidecar output file. The
// EnvSentinel guard makes the child skip its own daemonization. The parent does
// NOT Wait — the child keeps running after Spawn returns and the parent exits.
func reexecDetached(logPath string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	// A fresh Cmd per attempt: a failed Start must not be retried on the same Cmd.
	var cmd *exec.Cmd
	start := func(flags uint32) error {
		cmd = exec.Command(self, os.Args[1:]...)
		cmd.Env = append(os.Environ(), EnvSentinel+"=1")
		cmd.Stdin = nil // NUL
		cmd.Stdout = lf
		cmd.Stderr = lf
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		return cmd.Start()
	}

	if err := start(detachFlags | windows.CREATE_BREAKAWAY_FROM_JOB); err != nil {
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			_ = lf.Close()
			return nil, err
		}
		// Our job object forbids breakaway. Retry without the flag: the child then
		// shares our job, which still outlives us unless the host kills the job on
		// exit — strictly better than failing to daemonize at all.
		slog.Debug("daemon.breakaway_denied", "error", err)
		if err := start(detachFlags); err != nil {
			_ = lf.Close()
			return nil, err
		}
	}
	// The child inherited the handle; the parent no longer needs it.
	_ = lf.Close()
	return cmd, nil
}

// PIDAlive reports whether a live process with pid exists. ERROR_ACCESS_DENIED
// means it exists but belongs to another user / a higher integrity level, so it is
// alive; every other open error (notably ERROR_INVALID_PARAMETER, "no such pid")
// means it is not. A process that has exited keeps its pid until the handle is
// closed, hence the exit-code check rather than a successful open.
func PIDAlive(pid int) bool {
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

// NotifyStop creates this process's stop event and delivers a SIGTERM to ch when
// `serve stop` / `worker stop` sets it (see the file comment). A failure to create
// the event is not fatal — it only means this process cannot be stopped through
// the event, which is reported beside the process and never blocks startup.
func NotifyStop(ch chan<- os.Signal) {
	name := stopEventName(os.Getpid())
	h, err := windows.CreateEvent(nil, 1 /* manual reset */, 0 /* not signaled */, windows.StringToUTF16Ptr(name))
	if err != nil {
		slog.Warn("daemon.stop_event_unavailable", "event", name, "error", err)
		return
	}
	go func() {
		defer windows.CloseHandle(h)
		if _, err := windows.WaitForSingleObject(h, windows.INFINITE); err != nil {
			slog.Warn("daemon.stop_event_wait_failed", "event", name, "error", err)
			return
		}
		// The caller's channel is buffered and read by one goroutine; a full
		// channel means a stop is already pending, so dropping is correct.
		select {
		case ch <- syscall.SIGTERM:
		default:
		}
	}()
}

// Terminate asks the gofer process pid to shut down gracefully by setting its stop
// event. A missing event means pid is not a process this build daemonized (or the
// pid was reused) — a hard kill is then the operator's call, so the error carries
// KillHint rather than terminating anything here.
func Terminate(pid int) error {
	name := stopEventName(pid)
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, windows.StringToUTF16Ptr(name))
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return fmt.Errorf("stop event not found for pid=%d: not a gofer started by this build, or the pid was reused; if it is really gone remove the pidfile, otherwise %s", pid, KillHint(pid))
		}
		return fmt.Errorf("open stop event %s: %w", name, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.SetEvent(h); err != nil {
		return fmt.Errorf("set stop event %s: %w", name, err)
	}
	return nil
}

// KillHint is the manual hard kill an operator can run when a graceful stop
// failed or was refused.
func KillHint(pid int) string { return fmt.Sprintf("taskkill /PID %d /F", pid) }

// SessionInfo reports the Windows session of the current process, so startup logs
// say whether this gofer can reach the user's desktop: Interactive is true only
// when we run in the session attached to the console (session 0 = a service, which
// by design cannot touch the desktop).
func SessionInfo() Session {
	var id uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id); err != nil {
		return Session{}
	}
	console := windows.WTSGetActiveConsoleSessionId()
	return Session{ID: id, Interactive: id == console && console != noConsoleSession}
}
