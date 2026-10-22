package desktop

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/inhere/gofer/internal/util"
)

// The Windows toast PROGRAM (portable file on purpose: it is pure text plus its
// verdict mapping, so it is unit tested on every platform, not only on a Windows host).
//
// The script is FIXED: every piece of event data travels in the environment
// (GOFER_TOAST_XML), so no job title / prompt is ever interpolated into PowerShell
// source. The child cannot return a value on a pipe, so its verdict travels as the exit
// code plus one line of output (see the toastExit* constants).

// toastAppID is the AUMID the toast is posted under. WinRT refuses an id that is not
// REGISTERED in the Start Menu ("element not found"), so gofer borrows Windows
// PowerShell's own AUMID: PowerShell 5.1 is part of Windows 10/11, which makes a toast
// possible with neither a launcher shortcut nor a resident tray helper. That is the
// reason this approach is more reliable here than driving the WinRT API from Go (which
// would need a COM interop layer AND the same registered-id dance).
const toastAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// Exit codes of the fixed toast program.
const (
	// toastExitOK: the toast was handed to the shell.
	toastExitOK = 0
	// toastExitDisabled: ToastNotifier.Setting is not Enabled, i.e. the person (or a
	// policy) turned notifications off for this app. Show() would return without an
	// error and display nothing (measured on Windows 10/11), so the script reports the
	// setting itself instead of letting gofer claim the toast was shown.
	toastExitDisabled = 3
	// toastExitStartFailed: the child never produced an exit status (gofer could not
	// start it). Only the Go side produces this code; the script never exits with it.
	toastExitStartFailed = -1
)

// toastSettingPrefix tags the setting value the script prints before exiting with
// toastExitDisabled: "setting=DisabledForUser".
const toastSettingPrefix = "setting="

// toastDisabledHint tells the operator how to get notifications back. It is part of the
// skip reason, so the log line answers "why is nothing popping up" on its own.
const toastDisabledHint = "Windows 设置 → 系统 → 通知：打开通知总开关与 Windows PowerShell"

// toastScript is a FIXED program: event data comes from the environment, never from the
// source text. The Setting check runs BEFORE Show: with notifications switched off the
// Show() call succeeds silently, which is exactly the failure mode this check reports.
const toastScript = `$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$doc = New-Object Windows.Data.Xml.Dom.XmlDocument
$doc.LoadXml($env:` + toastXMLEnv + `)
$toast = New-Object Windows.UI.Notifications.ToastNotification $doc
$notifier = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + toastAppID + `')
$setting = [string]$notifier.Setting
if ($setting -ne 'Enabled') {
  Write-Output ('` + toastSettingPrefix + `' + $setting)
  exit 3
}
$notifier.Show($toast)
`

// toastScriptEncoded is toastScript as -EncodedCommand wants it: base64 of UTF-16LE.
// Encoding it once keeps every popup to a single argv slot with no quoting rules.
var toastScriptEncoded = encodePSCommand(toastScript)

// toastResult maps the child's exit code and output to the error Show returns: the
// agreed disabled code becomes a *SkipError naming the setting value, anything else is
// the popup command failing.
func toastResult(code int, out string) error {
	out = strings.TrimSpace(out)
	switch code {
	case toastExitOK:
		return nil
	case toastExitDisabled:
		return &SkipError{Reason: toastDisabledReason(toastSettingValue(out))}
	case toastExitStartFailed:
		return fmt.Errorf("powershell toast: %s", out)
	}
	return fmt.Errorf("powershell toast: exit %d: %s", code, out)
}

// toastSettingValue reads the setting value out of the child's output ("setting=X").
// Empty when the line is missing (an older / unexpected script), which the reason then
// states without guessing a value.
func toastSettingValue(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), toastSettingPrefix); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// toastDisabledReason builds the skip reason for a disabled setting: the Windows value
// plus how to turn notifications back on. The value is part of the key, so a changed
// setting (DisabledForUser → DisabledByGroupPolicy) is reported again.
func toastDisabledReason(setting string) string {
	if setting == "" {
		setting = "not Enabled"
	}
	return "windows notification setting " + setting + " (ToastNotifier.Setting) — " + toastDisabledHint
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
