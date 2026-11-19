// Package needsreply is the one definition of the 「needs a reply」 marker: the line an agent
// writes when it cannot continue without a person, the text that teaches the agent the
// line, and the parse of a finished turn's text. A terminal session's hook relay and a
// managed ACP session both read it, so the two cannot disagree on what counts.
package needsreply

import (
	"strings"

	"github.com/inhere/gofer/internal/msgprefix"
)

// Marker is the line an agent writes (alone, as the last line) when it
// cannot continue without a person's answer. Only such turns reach the "waiting
// for me" list; a plain report turn does not.
const Marker = "[needs-reply]"

// Reminder is the line SessionStart adds to additionalContext so the
// agent knows the marker. Generic wording: it names no project or tool.
var Reminder = msgprefix.Format(msgprefix.Hint, "", "If you cannot continue without the person's answer, write "+
	Marker+" alone on the last line of your reply. Do not write it when you are only reporting that work is done.")

// Split strips the marker line from msg. The marker counts only as the
// last non-empty line, compared exactly (case-sensitive) after trimming spaces; a
// copy in the middle of the text is ordinary prose. body is msg without that line
// (and the trailing blank lines before it).
func Split(msg string) (body string, needs bool) {
	trimmed := strings.TrimRight(msg, " \t\r\n")
	nl := strings.LastIndexByte(trimmed, '\n')
	last := trimmed[nl+1:] // nl == -1 → the whole text is one line
	if strings.TrimSpace(last) != Marker {
		return msg, false
	}
	if nl < 0 {
		return "", true
	}
	return strings.TrimRight(trimmed[:nl], " \t\r\n"), true
}
