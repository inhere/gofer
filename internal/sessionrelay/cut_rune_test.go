package sessionrelay

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestCutOnRuneBoundary(t *testing.T) {
	got := cutOnRune(strings.Repeat("a", maxTurnBody-1)+"中文结尾", maxTurnBody)
	if !utf8.ValidString(got) || got != strings.Repeat("a", maxTurnBody-1) {
		t.Fatalf("bad cut: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestSessionLabelCutsChineseTitleOnRuneBoundary(t *testing.T) {
	// 13 ASCII bytes + 3-byte runes: byte 40 falls inside a character.
	title := "proj-dev-a: x " + "没问题那个提交可以推现在我到"
	got := sessionLabel(jobstore.AgentSession{Title: title, SessionID: "sid"})
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || len(got) > 40+len("…") {
		t.Fatalf("sessionLabel = %q (valid=%v)", got, utf8.ValidString(got))
	}
}
