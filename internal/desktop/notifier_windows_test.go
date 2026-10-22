//go:build windows

package desktop

import (
	"errors"
	"strings"
	"testing"
)

// showToast is the whole popup minus the session-0 check: a stub executor drives every
// verdict without running PowerShell (no real toast in tests).
func TestShowToastMapsChildVerdict(t *testing.T) {
	tests := []struct {
		name      string
		code      int
		out       string
		started   bool
		wantSkip  string // substring of SkipError.Reason; empty = expect no skip
		wantError string // substring of a real error; empty = expect success
		wantNil   bool
	}{
		{name: "shown", code: toastExitOK, started: true, wantNil: true},
		{
			name: "settings disabled", code: toastExitDisabled, out: "setting=DisabledForUser\n", started: true,
			wantSkip: "DisabledForUser",
		},
		{
			name: "settings disabled by manifest", code: toastExitDisabled, out: "setting=DisabledByManifest", started: true,
			wantSkip: "DisabledByManifest",
		},
		{name: "powershell missing", started: false, wantSkip: "powershell.exe not found"},
		{name: "popup failed", code: 2, out: "element not found", started: true, wantError: "element not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotEnv map[string]string
			run := func(env map[string]string) (int, string, bool) {
				gotEnv = env
				return tc.code, tc.out, tc.started
			}
			err := showToast(run, Notification{Title: "job 等待验收：a", Text: "project p"})
			switch {
			case tc.wantNil:
				if err != nil {
					t.Fatalf("showToast = %v, want nil", err)
				}
			case tc.wantSkip != "":
				var skip *SkipError
				if !errors.As(err, &skip) {
					t.Fatalf("showToast = %v, want *SkipError", err)
				}
				if !strings.Contains(skip.Reason, tc.wantSkip) {
					t.Errorf("reason = %q, want it to contain %q", skip.Reason, tc.wantSkip)
				}
			default:
				var skip *SkipError
				if err == nil || errors.As(err, &skip) {
					t.Fatalf("showToast = %v, want a real (non-skip) error", err)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Errorf("error = %v, want it to contain %q", err, tc.wantError)
				}
			}
			// The event data reaches the child through the environment, never argv.
			if !strings.Contains(gotEnv[toastXMLEnv], "job 等待验收：a") {
				t.Errorf("%s = %q, want the toast XML", toastXMLEnv, gotEnv[toastXMLEnv])
			}
		})
	}
}
