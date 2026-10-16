package job

import (
	"strings"
	"testing"
)

// TestSplitInjectedRules pins the two halves the read paths show separately
// (gofer-chd9): the caller's own words, and the rules frame the server injected,
// which the job detail page offers behind a toggle instead of dropping it.
func TestSplitInjectedRules(t *testing.T) {
	t.Parallel()
	frame := rulesPromptHeader + "\n\n### house-rules\n- never push\n\n" + rulesPromptEnd
	cases := []struct {
		name         string
		prompt       string
		wantBody     string
		wantInjected string
	}{
		{
			name:         "injected section (plus the skills manifest behind it)",
			prompt:       frame + "\n\n## Skills\n- gofer-usage\n\nFix the login page.",
			wantBody:     "## Skills\n- gofer-usage\n\nFix the login page.",
			wantInjected: frame,
		},
		{
			name:     "nothing injected",
			prompt:   "Fix the login page.",
			wantBody: "Fix the login page.",
		},
		{
			// A prompt that merely QUOTES the header (a caller discussing gofer's
			// rules) has no end marker: it is not an injected section.
			name:     "quoted header only",
			prompt:   rulesPromptHeader + " is what gofer prepends.",
			wantBody: rulesPromptHeader + " is what gofer prepends.",
		},
		{
			name:     "empty prompt",
			prompt:   "",
			wantBody: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, injected := SplitInjectedRules(tc.prompt)
			if body != tc.wantBody {
				t.Fatalf("body = %q, want %q", body, tc.wantBody)
			}
			if injected != tc.wantInjected {
				t.Fatalf("injected = %q, want %q", injected, tc.wantInjected)
			}
			// The two halves must reconstruct the prompt (modulo the newline the
			// strip trims), so nothing is shown twice or lost.
			if injected != "" && strings.TrimLeft(injected+"\n\n"+body, "\n") != strings.TrimLeft(tc.prompt, "\n") {
				t.Fatalf("halves do not reconstruct the prompt:\n%q\n%q", injected+"\n\n"+body, tc.prompt)
			}
			// stripRulesSection is the same split, body only.
			if got := stripRulesSection(tc.prompt); got != body {
				t.Fatalf("stripRulesSection = %q, want %q", got, body)
			}
		})
	}
}
