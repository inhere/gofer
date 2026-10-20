package acp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateCutsOnRuneBoundary(t *testing.T) {
	got := truncate(strings.Repeat("a", maxRawBytes-1) + "中文结尾")
	if !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
	}
	if want := strings.Repeat("a", maxRawBytes-1) + "…(truncated)"; got != want {
		t.Fatalf("unexpected cut: %q", got)
	}
}
