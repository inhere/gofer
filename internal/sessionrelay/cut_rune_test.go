package sessionrelay

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCutOnRuneBoundary(t *testing.T) {
	got := cutOnRune(strings.Repeat("a", maxTurnBody-1)+"中文结尾", maxTurnBody)
	if !utf8.ValidString(got) || got != strings.Repeat("a", maxTurnBody-1) {
		t.Fatalf("bad cut: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}
