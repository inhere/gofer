package hookrelay

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/inhere/gofer/internal/sessionrelay"
)

// webReplyTail bounds the transcript scan for the web message a SendMessage
// answers: a long turn (big tool results) can sit between the two.
const webReplyTail = 4 << 20

// sendMessageTool is Claude Code's cross-session messaging tool.
const sendMessageTool = "SendMessage"

// webReplyOf reports the reply text and its address when this PostToolUse is the
// session answering a web message: a SendMessage whose recipient is the Claude
// Code address a "[来自 web，…]" message came from (the messenger). Everything
// else — messages to other sessions, structured (non-text) messages — is "".
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

// webMessageFrom reports whether the transcript holds a web message delivered
// from address: a non-assistant entry whose peer origin (top level, or the
// attachment of a message absorbed mid-turn) has from == address and a body that
// starts with the web prefix. Claude Code 2.1.296 writes
// origin={kind:"peer", from, name, body, msg_id}; when that shape changes, a line
// carrying both `from=\"<address>\"` and the web prefix still counts.
func webMessageFrom(path, address string) bool {
	lines, err := transcriptTailLines(path, webReplyTail)
	if err != nil {
		return false
	}
	addr := []byte(address)
	prefix := []byte(sessionrelay.WebMessagePrefix)
	quotedFrom := []byte(`from=\"` + address + `\"`)
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !bytes.Contains(line, addr) || !bytes.Contains(line, prefix) {
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
			if o != nil && o.Kind == "peer" && o.From == address && strings.HasPrefix(strings.TrimSpace(o.Body), sessionrelay.WebMessagePrefix) {
				return true
			}
		}
		if bytes.Contains(line, quotedFrom) {
			return true
		}
	}
	return false
}

type replyOrigin struct {
	Kind string `json:"kind"`
	From string `json:"from"`
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
		text = truncate(text, r.opts.MaxMessage)
	}
	if _, err := r.api.PostSessionReply(r.p.SessionID, text, to); err != nil {
		r.log("report web reply failed: %v", err)
		return
	}
	r.log("web reply reported (%d bytes, to %s)", len(text), to)
}
