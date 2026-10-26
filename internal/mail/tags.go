package mail

import (
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
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
)

// WakeNotice is the default-language compatibility value.
var WakeNotice = i18n.T(i18n.Default, "mail.wake.notice")

// RenderWake renders the wake prompt for count pending letters in the default language.
func RenderWake(count int) string { return RenderWakeLang(i18n.Default, count) }

// RenderWakeLang renders the wake prompt in lang. The XML marker remains language-neutral.
func RenderWakeLang(lang i18n.Lang, count int) string {
	return "<" + TagWake + ` count="` + strconv.Itoa(count) + `"/>` + "\n" +
		i18n.T(lang, "mail.wake.notice")
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
	// BroadcastID / Recipients mark a copy of a broadcast (设计 2026-10-22「D2」): the agent then
	// knows it is one of several recipients instead of the only one. 0 / 0 = a single letter.
	BroadcastID int64
	Recipients  int
}

// RenderLetter renders one letter in the default language.
func RenderLetter(l Letter) string { return RenderLetterLang(i18n.Default, l) }

// RenderLetterLang renders one letter as the fixed harness tag in lang.
func RenderLetterLang(lang i18n.Lang, l Letter) string {
	body := strings.ReplaceAll(l.Body, "</"+TagLetter, `<\/`+TagLetter)
	if l.Truncated {
		body += "\n" + i18n.T(lang, "mail.letter.truncated", "id", l.ID)
	}
	return "<" + TagLetter + ` id="` + strconv.FormatInt(l.ID, 10) + `" from="` + tagAttr(l.From) +
		`" topic="` + tagAttr(l.Topic) + `" urgent="` + strconv.FormatBool(l.Urgent) + `"` +
		broadcastAttrs(l) + ">" + body + "</" + TagLetter + ">"
}

// broadcastAttrs renders the broadcast / recipients attributes of a broadcast copy, nothing at
// all for a single letter.
func broadcastAttrs(l Letter) string {
	if l.BroadcastID <= 0 {
		return ""
	}
	return ` broadcast="b` + strconv.FormatInt(l.BroadcastID, 10) + `" recipients="` + strconv.Itoa(l.Recipients) + `"`
}

func tagAttr(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(v)
}
