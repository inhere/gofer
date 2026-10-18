package mail

import (
	"strconv"
	"strings"
)

// The injected-letter tags (mailbox design §二). Letters are NOT user input: the hook and the
// server deliverer both render them from here — the single source of truth — and
// hookrelay.IsHarnessPrompt recognises them, so a mail turn never counts as the human
// returning (no relay auto-off, no title rewrite). hookrelay re-exports these names.
const (
	// TagLetter is the element wrapping one injected letter: `<gofer-mail ...>body</gofer-mail>`.
	TagLetter = "gofer-mail"
	// TagWake is the self-closing wake marker: `<gofer-mail-wake count="N"/>`.
	TagWake = "gofer-mail-wake"
	// WakeNotice is the fixed one-line notice following the wake tag.
	WakeNotice = "有待读信件，将在本轮开始时注入，无需回复"
)

// RenderWake renders the wake prompt for count pending letters.
func RenderWake(count int) string {
	return "<" + TagWake + ` count="` + strconv.Itoa(count) + `"/>` + "\n" + WakeNotice
}

// Letter is what the tag shows of one letter: the id (so a recipient can tell a repeat of an
// at-least-once delivery), the sender's display and topic, the urgency and the body.
type Letter struct {
	ID        int64
	From      string
	Topic     string
	Urgent    bool
	Body      string
	Truncated bool
}

// RenderLetter renders one letter as the fixed harness tag, so the model (and
// IsHarnessPrompt) can tell a letter from something a person typed. Attribute values are
// escaped; a body that contains the closing tag cannot end the element early.
func RenderLetter(l Letter) string {
	body := strings.ReplaceAll(l.Body, "</"+TagLetter, `<\/`+TagLetter)
	if l.Truncated {
		body += "\n…（正文已截断，完整内容：gofer session mail read " + strconv.FormatInt(l.ID, 10) + "）"
	}
	return "<" + TagLetter + ` id="` + strconv.FormatInt(l.ID, 10) + `" from="` + tagAttr(l.From) +
		`" topic="` + tagAttr(l.Topic) + `" urgent="` + strconv.FormatBool(l.Urgent) + `">` +
		body + "</" + TagLetter + ">"
}

func tagAttr(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(v)
}
