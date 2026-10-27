//go:build darwin

package desktop

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/inhere/gofer/internal/procattr"
)

// NewSystemNotifier returns the macOS implementation: `osascript -e 'display
// notification …'`, which is built into macOS (no tool to install).
func NewSystemNotifier() Notifier { return darwinNotifier{} }

// PlatformSupported reports whether this build can pop a desktop notification at all.
func PlatformSupported() bool { return true }

type darwinNotifier struct{}

func (darwinNotifier) Show(n Notification) error {
	exe, err := exec.LookPath("osascript")
	if err != nil {
		return &SkipError{Reason: "osascript not found"}
	}
	text := strings.TrimSpace(n.Text)
	if link := strings.TrimSpace(n.Link); link != "" {
		if text != "" {
			text += "\n"
		}
		text += link
	}
	// display notification has no click target: the link goes into the body, so the
	// person can read it there (macOS does not let a plain script set an action).
	script := "display notification " + appleScriptString(text) + " with title " + appleScriptString(n.Title)
	cmd := exec.Command(exe, "-e", script)
	procattr.Background(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript display notification: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// appleScriptString quotes s as an AppleScript string literal. A newline stays a
// literal newline: AppleScript accepts it inside a quoted string.
func appleScriptString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
