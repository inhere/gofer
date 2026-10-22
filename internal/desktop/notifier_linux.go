//go:build linux

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/inhere/gofer/internal/procattr"
)

// NewSystemNotifier returns the Linux implementation: `notify-send` (libnotify), the
// desktop's own notification daemon. A host without it skips — the channel is optional
// and must never turn a missing tool into an error the job path cares about.
func NewSystemNotifier() Notifier { return linuxNotifier{} }

type linuxNotifier struct{}

func (linuxNotifier) Show(n Notification) error {
	if err := requireDesktopSession(); err != nil {
		return err
	}
	exe, err := exec.LookPath("notify-send")
	if err != nil {
		return &SkipError{Reason: "notify-send not installed (libnotify)"}
	}
	text := strings.TrimSpace(n.Text)
	if link := strings.TrimSpace(n.Link); link != "" {
		if text != "" {
			text += "\n"
		}
		text += link
	}
	// notify-send has no click action; the link rides along in the body.
	cmd := exec.Command(exe, "--app-name=gofer", n.Title, text)
	procattr.Background(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("notify-send: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// requireDesktopSession rejects a host with no graphical session at all (a container, a
// headless box): notify-send would fail with an opaque D-Bus error on every event.
func requireDesktopSession() error {
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return nil
	}
	return &SkipError{Reason: "no desktop session (DISPLAY / WAYLAND_DISPLAY / DBUS_SESSION_BUS_ADDRESS unset)"}
}
