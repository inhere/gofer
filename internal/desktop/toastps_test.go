package desktop

import (
	"errors"
	"strings"
	"testing"
)

// The agreed disabled exit code maps to a SkipError whose reason names the Windows
// setting value and how to turn notifications back on: that reason is the only trace a
// disabled toast leaves in the log.
func TestToastResultMapsDisabledSettingToSkip(t *testing.T) {
	err := toastResult(toastExitDisabled, "setting=DisabledForUser\n")
	var skip *SkipError
	if !errors.As(err, &skip) {
		t.Fatalf("toastResult(disabled) = %v, want *SkipError", err)
	}
	for _, want := range []string{"DisabledForUser", "Windows 设置 → 系统 → 通知", "Windows PowerShell"} {
		if !strings.Contains(skip.Reason, want) {
			t.Errorf("reason = %q, want it to contain %q", skip.Reason, want)
		}
	}
	// A different setting value is a different reason, so the dispatcher reports it
	// again after a policy change instead of staying silent.
	other := toastResult(toastExitDisabled, "setting=DisabledByGroupPolicy")
	var skip2 *SkipError
	if !errors.As(other, &skip2) {
		t.Fatalf("toastResult(disabled by policy) = %v, want *SkipError", other)
	}
	if skip2.Reason == skip.Reason {
		t.Errorf("reason for DisabledByGroupPolicy = %q, want it to differ from %q", skip2.Reason, skip.Reason)
	}
	if !strings.Contains(skip2.Reason, "DisabledByGroupPolicy") {
		t.Errorf("reason = %q, want the policy value", skip2.Reason)
	}
}

// The disabled code without the value line still skips (an unexpected script must not be
// reported as a real failure), and says so rather than inventing a setting.
func TestToastResultDisabledWithoutSettingLine(t *testing.T) {
	err := toastResult(toastExitDisabled, "")
	var skip *SkipError
	if !errors.As(err, &skip) {
		t.Fatalf("toastResult(disabled, no output) = %v, want *SkipError", err)
	}
	if !strings.Contains(skip.Reason, "not Enabled") {
		t.Errorf("reason = %q, want it to say the setting is not Enabled", skip.Reason)
	}
}

// Success and real failures keep their old shape: only the disabled setting is a skip.
func TestToastResultSuccessAndFailure(t *testing.T) {
	if err := toastResult(toastExitOK, ""); err != nil {
		t.Errorf("toastResult(ok) = %v, want nil", err)
	}
	err := toastResult(2, "boom")
	var skip *SkipError
	if err == nil || errors.As(err, &skip) {
		t.Fatalf("toastResult(exit 2) = %v, want a real (non-skip) error", err)
	}
	if !strings.Contains(err.Error(), "exit 2") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want the exit code and the child output", err)
	}
}

// toastSettingValue reads the tagged line out of whatever else the child wrote.
func TestToastSettingValue(t *testing.T) {
	for _, tc := range []struct{ out, want string }{
		{"setting=DisabledForUser\n", "DisabledForUser"},
		{"warning: something\n  setting=DisabledByManifest  \n", "DisabledByManifest"},
		{"", ""},
		{"nothing tagged", ""},
	} {
		if got := toastSettingValue(tc.out); got != tc.want {
			t.Errorf("toastSettingValue(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

// The script checks ToastNotifier.Setting BEFORE it calls Show: Show() on a disabled
// setting returns without an error and displays nothing, so a script that only shows
// would report a phantom success. The check must also exit with the agreed code and
// print the tagged value the Go side reads.
func TestToastScriptChecksSettingBeforeShow(t *testing.T) {
	script := toastScript
	check := strings.Index(script, "$notifier.Setting")
	show := strings.Index(script, "$notifier.Show(")
	if check < 0 {
		t.Fatalf("toast script has no ToastNotifier.Setting check:\n%s", script)
	}
	if show < 0 || check > show {
		t.Errorf("toast script checks the setting after Show (check=%d, show=%d):\n%s", check, show, script)
	}
	for _, want := range []string{
		"if ($setting -ne 'Enabled')",
		"exit 3",
		"Write-Output ('setting=' + $setting)",
		"$env:" + toastXMLEnv,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("toast script missing %q:\n%s", want, script)
		}
	}
	// The exit code in the script and the constant that maps it must not drift apart.
	if toastExitDisabled != 3 {
		t.Errorf("toastExitDisabled = %d, want 3 (the code the script exits with)", toastExitDisabled)
	}
}
