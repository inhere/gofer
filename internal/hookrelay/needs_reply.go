package hookrelay

import (
	"strings"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/msgprefix"
)

// NeedsReplyMarker is the line an agent writes (alone, as the last line) when it
// cannot continue without a person's answer. Only such turns reach the "waiting
// for me" list; a plain report turn does not.
const NeedsReplyMarker = "[needs-reply]"

// NeedsReplyReminder is the line SessionStart adds to additionalContext so the
// agent knows the marker. Generic wording: it names no project or tool.
var NeedsReplyReminder = msgprefix.Format(msgprefix.Hint, "", "If you cannot continue without the person's answer, write "+
	NeedsReplyMarker+" alone on the last line of your reply. Do not write it when you are only reporting that work is done.")

// splitNeedsReply strips the marker line from msg. The marker counts only as the
// last non-empty line, compared exactly (case-sensitive) after trimming spaces; a
// copy in the middle of the text is ordinary prose. body is msg without that line
// (and the trailing blank lines before it).
func splitNeedsReply(msg string) (body string, needs bool) {
	trimmed := strings.TrimRight(msg, " \t\r\n")
	nl := strings.LastIndexByte(trimmed, '\n')
	last := trimmed[nl+1:] // nl == -1 → the whole text is one line
	if strings.TrimSpace(last) != NeedsReplyMarker {
		return msg, false
	}
	if nl < 0 {
		return "", true
	}
	return strings.TrimRight(trimmed[:nl], " \t\r\n"), true
}

// needsReplyReminder appends NeedsReplyReminder to the SessionStart context after
// whatever is already there, only for agents whose hook output reaches the model.
func (r *runner) needsReplyReminder(res Result, _ client.AgentSession) Result {
	if !CatchUpAgent(r.p.dialect()) {
		return res
	}
	if res.Context != "" {
		res.Context += "\n\n"
	}
	res.Context += NeedsReplyReminder
	return res
}
