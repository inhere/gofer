//go:build windows

package desktop

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/util"
)

// NewSystemNotifier returns the Windows implementation: a Windows.UI.Notifications
// toast, posted by a short PowerShell child.
func NewSystemNotifier() Notifier { return windowsNotifier{} }

// toastAppID is the AUMID the toast is posted under. WinRT refuses an id that is not
// REGISTERED in the Start Menu ("element not found"), so gofer borrows Windows
// PowerShell's own AUMID: PowerShell 5.1 is part of Windows 10/11, which makes a toast
// possible with neither a launcher shortcut nor a resident tray helper. That is the
// reason this approach is more reliable here than driving the WinRT API from Go (which
// would need a COM interop layer AND the same registered-id dance).
const toastAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// toastScript is a FIXED program: every piece of event data travels in the
// environment (GOFER_TOAST_XML), so no job title / prompt is ever interpolated into
// PowerShell source.
const toastScript = `$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$doc = New-Object Windows.Data.Xml.Dom.XmlDocument
$doc.LoadXml($env:` + toastXMLEnv + `)
$toast = New-Object Windows.UI.Notifications.ToastNotification $doc
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + toastAppID + `').Show($toast)
`

// toastScriptEncoded is toastScript as -EncodedCommand wants it: base64 of UTF-16LE.
// Encoding it once keeps every popup to a single argv slot with no quoting rules.
var toastScriptEncoded = encodePSCommand(toastScript)

type windowsNotifier struct{}

func (windowsNotifier) Show(n Notification) error {
	if err := requireInteractiveSession(); err != nil {
		return err
	}
	exe, err := exec.LookPath("powershell.exe")
	if err != nil {
		return &SkipError{Reason: "powershell.exe not found"}
	}
	cmd := exec.Command(exe, "-NoProfile", "-NonInteractive", "-EncodedCommand", toastScriptEncoded)
	procattr.Background(cmd)
	cmd.Env = util.Environ(map[string]string{toastXMLEnv: toastXML(n)})
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("powershell toast: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
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

// encodePSCommand base64-encodes script as UTF-16LE, the only encoding
// `powershell -EncodedCommand` accepts.
func encodePSCommand(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, 0, util.CapMul(len(units), 2))
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}
