package secret

import (
	"strings"
	"testing"
)

func TestSubmitSecretWarning(t *testing.T) {
	warnings := ScanSubmission([]string{"printf", "AKIA1234567890ABCDEF"}, []string{"--token", "sk-abcdefghijklmnopqrstuvwxyz"}, "prompt password=abcdefghijklmnop")
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v, want command/args/prompt locations", warnings)
	}
	for _, warning := range warnings {
		if strings.Contains(warning, "AKIA") || strings.Contains(warning, "sk-") || strings.Contains(warning, "abcdefghijklmnop") {
			t.Fatalf("warning echoed secret: %q", warning)
		}
	}
	if got := ScanSubmission([]string{"echo", "safe"}, nil, "plain prompt"); len(got) != 0 {
		t.Fatalf("safe submission warnings = %v", got)
	}
}
