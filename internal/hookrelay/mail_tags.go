package hookrelay

import "strconv"

// Mail injection tags (mailbox design §二). Letters are NOT user input: the
// server deliverer and the UserPromptSubmit hook render them from here, and
// IsHarnessPrompt recognises them so a mail turn never counts as the human
// returning (no relay auto-off, no title rewrite).
const (
	// MailTag is the element wrapping one injected letter:
	// `<gofer-mail ...>body</gofer-mail>`.
	MailTag = "gofer-mail"
	// MailWakeTag is the self-closing wake marker: `<gofer-mail-wake count="N"/>`.
	MailWakeTag = "gofer-mail-wake"
	// MailWakeNotice is the fixed one-line notice following the wake tag.
	MailWakeNotice = "有待读信件，将在本轮开始时注入，无需回复"
)

// RenderMailWake renders the wake prompt for count pending letters.
func RenderMailWake(count int) string {
	return "<" + MailWakeTag + ` count="` + strconv.Itoa(count) + `"/>` + "\n" + MailWakeNotice
}

// isMailInjection reports whether t (already trimmed) starts with a mail block
// or the wake tag. Tight on purpose: the tag must open the prompt, and the
// tag name must be followed by a delimiter (so `<gofer-mailx` does not match).
func isMailInjection(t string) bool {
	return hasTagAt(t, MailTag, ' ', '\t', '\n', '\r', '>') && containsClose(t) ||
		hasTagAt(t, MailWakeTag, ' ', '\t', '\n', '\r', '/', '>')
}

func containsClose(t string) bool {
	return len(t) >= len("</"+MailTag+">") && t[len(t)-len("</"+MailTag+">"):] == "</"+MailTag+">"
}

func hasTagAt(t, name string, delims ...byte) bool {
	p := "<" + name
	if len(t) <= len(p) || t[:len(p)] != p {
		return false
	}
	for _, d := range delims {
		if t[len(p)] == d {
			return true
		}
	}
	return false
}
