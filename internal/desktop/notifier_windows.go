//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"

	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/util"
)

// NewSystemNotifier returns the Windows implementation: a Windows.UI.Notifications
// toast, posted by a short PowerShell child.
func NewSystemNotifier() Notifier { return windowsNotifier{} }

type windowsNotifier struct{}

// toastExecutor runs the fixed toast program (toastps.go) with the event data in env
// and reports the child's exit code and combined output. It is a package variable only
// so a test can exercise the verdict mapping (toastResult) through showToast without
// popping a real toast. started is false when no powershell.exe could be launched.
type toastExecutor func(env map[string]string) (code int, out string, started bool)

var runToast toastExecutor = execToast

func (windowsNotifier) Show(n Notification) error {
	if err := requireInteractiveSession(); err != nil {
		return err
	}
	return showToast(runToast, n)
}

// showToast separates "can this machine pop at all" (Show's session check) from "what
// did the child say": the mapping is then testable with a stub executor.
func showToast(run toastExecutor, n Notification) error {
	code, out, started := run(map[string]string{toastXMLEnv: toastXML(n)})
	if !started {
		return &SkipError{Reason: "powershell.exe not found"}
	}
	return toastResult(code, out)
}

// execToast is the production executor: one short, detached PowerShell running the
// fixed script. Every argv item is constant, so no event data reaches the command line.
func execToast(env map[string]string) (int, string, bool) {
	exe, err := exec.LookPath("powershell.exe")
	if err != nil {
		return 0, "", false
	}
	cmd := exec.Command(exe, "-NoProfile", "-NonInteractive", "-EncodedCommand", toastScriptEncoded)
	procattr.Background(cmd)
	cmd.Env = util.Environ(env)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return toastExitOK, string(out), true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), string(out), true
	}
	return toastExitStartFailed, err.Error() + ": " + string(out), true
}

// requireInteractiveSession rejects the one Windows context where a toast can never
// appear: session 0. A service, or a scheduled task configured to run whether the user
// is logged on, lives there and owns no desktop. A task with an InteractiveToken (what
// `gofer serve install` creates) runs in the logged-on user's session and passes.
func requireInteractiveSession() error {
	var sid uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid); err != nil {
		return fmt.Errorf("query session id: %w", err)
	}
	if sid == 0 {
		return &SkipError{Reason: "windows session 0 (service session, no interactive desktop)"}
	}
	return nil
}
