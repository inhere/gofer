package acp

import (
	"strings"

	"github.com/inhere/gofer/internal/needsreply"
)

// The needs-reply marker of a resident turn. The agent writes it as the last non-empty
// line of its final message; the runner holds back the tail of the stream that could still
// become that line, so the marker never reaches stdout.log or acp.jsonl, and reports the
// fact to the job layer (ACPRequest.OnTurnNeedsReply) instead.

// holdMarker takes one agent-message chunk and returns the part that may be shown now. The
// trailing text that could still turn out to be the marker line (and the blank space around
// it) stays in h.held until more text arrives, a boundary releases it, or the turn ends.
// Without hold mode (a one-shot job) the chunk passes through untouched.
func (h *handler) holdMarker(chunk string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.needsReplyHold {
		return chunk
	}
	text := h.held + chunk
	keep := heldSuffixLen(text)
	h.held = text[len(text)-keep:]
	return text[:len(text)-keep]
}

// heldSuffixLen is how many trailing bytes of text must wait: all trailing whitespace, and
// when the last non-empty line is a prefix of the marker, that line with the blank lines
// before it (they are dropped with the marker).
func heldSuffixLen(text string) int {
	const space = " \t\r\n"
	end := len(strings.TrimRight(text, space))
	if end == 0 {
		return len(text)
	}
	lineStart := strings.LastIndexByte(text[:end], '\n') + 1
	if last := strings.TrimSpace(text[lineStart:end]); last != "" && strings.HasPrefix(needsreply.Marker, last) {
		from := lineStart
		for from > 0 && strings.IndexByte(space, text[from-1]) >= 0 {
			from--
		}
		return len(text) - from
	}
	return len(text) - end
}

// releaseHeld writes out what holdMarker kept back: the agent went on (a tool call, a plan)
// so the held text was not the end of the turn.
func (h *handler) releaseHeld() {
	h.mu.Lock()
	text := h.held
	h.held = ""
	h.mu.Unlock()
	if text != "" {
		h.writeStdout(text)
		h.addMessage(text)
	}
}

// finishTurnMarker settles the held tail when the turn is over: the marker line alone is
// dropped and remembered, anything else is released as the text it is.
func (h *handler) finishTurnMarker() {
	h.mu.Lock()
	text := h.held
	h.held = ""
	if body, needs := needsreply.Split(text); needs && body == "" {
		h.turnNeedsReply = true
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	if text != "" {
		h.writeStdout(text)
		h.addMessage(text)
	}
}

// takeNeedsReply reports whether the turn that just ended carried the marker, and clears it.
func (h *handler) takeNeedsReply() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.turnNeedsReply
	h.turnNeedsReply = false
	return v
}

// setNeedsReplyHold switches the marker hold on: a resident session's turns are the ones
// whose last line may be the marker.
func (h *handler) setNeedsReplyHold() {
	h.mu.Lock()
	h.needsReplyHold = true
	h.mu.Unlock()
}
