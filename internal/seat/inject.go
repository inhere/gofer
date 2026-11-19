package seat

import (
	"strings"

	"github.com/inhere/gofer/internal/i18n"
)

// The SessionStart injection of a seat (design 2026-11-18 resident-sessions §4): a session that
// sits in a seat is told, in every start / resume / compact, which position it holds and what
// the seat's charter asks of it — plus, exactly once, the continuation text that says where the
// earlier conversation stopped. The rendering lives here so the hook (internal/hookrelay) and
// the server (session view) cannot drift apart; the hook only calls Section.

// CharterBudget is how many bytes of a charter reach a session (the view and the injection use
// the same bound).
const CharterBudget = 4000

// SplitCharter tells the two spellings of a seat's charter apart: a value starting with "@" is
// a repository-relative path to the file that holds it (path comes back without the "@", text
// is empty), anything else is the text itself.
func SplitCharter(charter string) (path, text string) {
	s := strings.TrimSpace(charter)
	if rest := strings.TrimPrefix(s, "@"); rest != s {
		return strings.TrimSpace(rest), ""
	}
	return "", charter
}

// Cut cuts s to at most max bytes without splitting a rune, marking the cut.
func Cut(s string, max int) string { return cutRunes(s, max) }

// JoinRootPath appends a repository-relative slash path to a root, keeping the root's own
// separator style (a Windows root stays backslashed).
func JoinRootPath(root, rel string) string { return joinHostPath(root, rel) }

// SectionInput is what a hook knows about the seat a session sits in: the name and purpose, the
// charter text (already read from its file when the charter is a path) and the one-time
// continuation text the server handed out with the registration answer.
type SectionInput struct {
	Name         string
	Purpose      string
	Charter      string
	Continuation string
	Lang         i18n.Lang
}

// Section renders the seat block of the SessionStart context: the heading with the name and
// purpose, the charter and — for the first start of a session that has just taken the seat —
// the continuation text. An empty name renders nothing.
func Section(in SectionInput) string {
	lang := in.Lang
	if lang == "" {
		lang = i18n.Default
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	if p := strings.TrimSpace(in.Purpose); p != "" {
		b.WriteString(i18n.T(lang, "seat.hook.heading_purpose", "name", name, "purpose", p))
	} else {
		b.WriteString(i18n.T(lang, "seat.hook.heading", "name", name))
	}
	if c := strings.TrimSpace(in.Charter); c != "" {
		b.WriteString("\n" + i18n.T(lang, "seat.continuation.charter") + "\n" + cutRunes(c, CharterBudget))
	}
	if cont := strings.TrimSpace(in.Continuation); cont != "" {
		b.WriteString("\n\n" + cutRunes(cont, continuationBudget))
	}
	return b.String()
}
