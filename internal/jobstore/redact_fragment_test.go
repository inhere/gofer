package jobstore

import (
	"strings"
	"testing"
)

// TestRedactCatchesTruncatedFragments: an auto title or preview is cut to a fixed
// width, so it can hold most of a secret without the whole literal. Such a
// fragment must be redacted (and counted) too, or scan output and the stored
// title keep leaking the value.
func TestRedactCatchesTruncatedFragments(t *testing.T) {
	const secret = "YsweepFake8899secretQWERTYzx"
	cases := map[string]string{
		"head kept (title cut)":   `{"title":"echo cmd-` + secret[:23] + `"}`,
		"tail kept (preview cut)": "…" + secret[10:] + " done",
		"whole":                   "x " + secret + " y",
	}
	for name, text := range cases {
		got, n := redactString(text, []string{secret}, nil)
		if n == 0 || strings.Contains(got, secret[:14]) || strings.Contains(got, secret[len(secret)-14:]) {
			t.Fatalf("%s: got %q (count %d), want fragment redacted", name, got, n)
		}
	}
	// Short overlaps are ordinary text, not a leak: keep them.
	if got, n := redactString("YsweepF and zx", []string{secret}, nil); n != 0 || got != "YsweepF and zx" {
		t.Fatalf("short overlap redacted: %q (%d)", got, n)
	}
}
