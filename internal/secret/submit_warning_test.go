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

func TestSubmitSecretWarningCoversTitleAndTags(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		tags        []string
	}{
		{name: "title", title: "deploy token=abcdefghijklmnop"},
		{name: "tags", tags: []string{"safe", "AKIA1234567890ABCDEF"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warnings := ScanSubmissionWithMetadata(nil, nil, "", tc.title, tc.tags)
			if len(warnings) != 1 || warnings[0] == tc.title {
				t.Fatalf("warnings = %v, want one location without secret", warnings)
			}
			if strings.Contains(strings.Join(warnings, " "), "AKIA") || strings.Contains(strings.Join(warnings, " "), "abcdefghijklmnop") {
				t.Fatalf("warning echoed secret: %v", warnings)
			}
		})
	}
}
