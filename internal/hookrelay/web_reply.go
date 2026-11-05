package hookrelay

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/inhere/gofer/internal/msgprefix"
)

// webReplyTail bounds the transcript scan for the web message a SendMessage
// answers: a long turn (big tool results) can sit between the two.
const webReplyTail = 4 << 20

// sendMessageTool is Claude Code's cross-session messaging tool.
const sendMessageTool = "SendMessage"

// mailWakeOpen opens the mail wake notice a messenger forwards: gofer speaking, not a
// person, although it wears no system prefix.
const mailWakeOpen = "<gofer-mail-wake"

// webReplyOf reports the reply text and its address when this PostToolUse is the
// session answering a web message: a SendMessage whose recipient is the Claude
// Code address a "[来自 web，…]" message came from (the messenger). Everything
// else — messages to other sessions, structured (non-text) messages, and an answer
// to a gofer system message ("[gofer · …]", gofer-11gk), which is not a person —
// is "".
func webReplyOf(p Payload) (text, to string) {
	if p.ToolName != sendMessageTool || strings.TrimSpace(p.TranscriptPath) == "" || len(p.ToolInput) == 0 {
		return "", ""
	}
	var in struct {
		To        string          `json:"to"`
		Recipient string          `json:"recipient"`
		Message   json.RawMessage `json:"message"`
	}
	if json.Unmarshal(p.ToolInput, &in) != nil {
		return "", ""
	}
	to = strings.TrimSpace(in.To)
	if to == "" {
		to = strings.TrimSpace(in.Recipient)
	}
	if json.Unmarshal(in.Message, &text) != nil || strings.TrimSpace(text) == "" || to == "" {
		return "", ""
	}
	if !webMessageFrom(p.TranscriptPath, to) {
		return "", ""
	}
	return strings.TrimSpace(text), to
}

// webMessageBody reports whether a peer message body is a person's web message: it
// wears a person's prefix and is not a gofer system message. The two halves of the
// table (msgprefix) decide it, so a notice can never be read as the person having
// written — neither here nor in the Stop wait (gofer-11gk).
func webMessageBody(body string) bool {
	return msgprefix.IsPerson(body)
}

// webMessageFrom reports whether the transcript holds a web message delivered
// from address: a non-assistant entry whose peer origin (top level, or the
// attachment of a message absorbed mid-turn) has from == address and a body that
// is a person's web message. Claude Code 2.1.296 writes
// origin={kind:"peer", from, name, body, msg_id}; when that shape changes, a line
// carrying both `from=\"<address>\"` and the web prefix still counts.
//
// The address is searched in its JSON-escaped form too: a transcript line is JSON, so a
// Windows pipe's backslashes are doubled there. And a body counts when the messenger sent
// it, prefix or not: a model may forward the words without the prefix it was told to
// keep, and it is still the person speaking (messengerBody). A session may also answer
// the messenger by name ("gofer-web", or "gofer-web [ref]" after an ambiguity) instead of
// by the address: then any message the messenger forwarded is what it answers.
func webMessageFrom(path, address string) bool {
	lines, err := transcriptTailLines(path, webReplyTail)
	if err != nil {
		return false
	}
	addr := []byte(address)
	escaped := jsonEscaped(address)
	prefixes := personOpens()
	messenger := []byte(msgprefix.MessengerName)
	byName := address == msgprefix.MessengerName || strings.HasPrefix(address, msgprefix.MessengerName+" [")
	if byName {
		addr, escaped = messenger, messenger
	}
	quotedFrom := []byte(`from=\"` + string(escaped) + `\"`)
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !bytes.Contains(line, addr) && !bytes.Contains(line, escaped) {
			continue
		}
		prefixed := containsAny(line, prefixes)
		if !prefixed && !bytes.Contains(line, messenger) {
			continue
		}
		var entry struct {
			Type       string       `json:"type"`
			Origin     *replyOrigin `json:"origin"`
			Attachment *struct {
				Origin *replyOrigin `json:"origin"`
			} `json:"attachment"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type == "assistant" {
			continue
		}
		for _, o := range []*replyOrigin{entry.Origin, attachmentOrigin(entry.Attachment)} {
			if o != nil && o.Kind == "peer" && (o.From == address || (byName && o.Name == msgprefix.MessengerName)) &&
				(webMessageBody(o.Body) || messengerBody(o)) {
				return true
			}
		}
		if prefixed && bytes.Contains(line, quotedFrom) {
			return true
		}
	}
	return false
}

// messengerBody reports a body the messenger forwarded without a person's prefix: the
// words are a person's unless they are gofer's own (a system notice, a mail wake).
func messengerBody(o *replyOrigin) bool {
	body := strings.TrimSpace(o.Body)
	return o.Name == msgprefix.MessengerName && !msgprefix.IsSystem(body) && !strings.HasPrefix(body, mailWakeOpen)
}

// jsonEscaped is s as it appears inside a JSON string.
func jsonEscaped(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return []byte(s)
	}
	return b[1 : len(b)-1]
}

// personOpens lists the opening words of a person's message in every language: the
// session holds whatever language the server wrote when the message arrived, and
// this quick filter must not be narrower than webMessageBody.
func personOpens() [][]byte {
	var out [][]byte
	for _, kind := range []string{msgprefix.WebMessage, msgprefix.WebReply} {
		for _, open := range msgprefix.PersonOpens(kind) {
			out = append(out, []byte(open))
		}
	}
	return out
}

func containsAny(line []byte, subs [][]byte) bool {
	for _, sub := range subs {
		if bytes.Contains(line, sub) {
			return true
		}
	}
	return false
}

type replyOrigin struct {
	Kind string `json:"kind"`
	From string `json:"from"`
	Name string `json:"name"`
	Body string `json:"body"`
}

func attachmentOrigin(a *struct {
	Origin *replyOrigin `json:"origin"`
}) *replyOrigin {
	if a == nil {
		return nil
	}
	return a.Origin
}

// reportWebReply posts the session's answer to a web message (gofer-6er0), so the
// web conversation shows the session's own words instead of the messenger's
// retelling. Best effort: a failure is only logged.
func (r *runner) reportWebReply() {
	if r.p.dialect() != AgentClaude {
		return
	}
	text, to := webReplyOf(r.p)
	if text == "" {
		return
	}
	if len(text) > r.opts.MaxMessage {
		text = truncate(r.lang(), text, r.opts.MaxMessage)
	}
	if _, err := r.api.PostSessionReply(r.p.SessionID, text, to); err != nil {
		r.log("report web reply failed: %v", err)
		return
	}
	r.log("web reply reported (%d bytes, to %s)", len(text), to)
}
