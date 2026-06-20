package client

import "strings"

// SSEEvent is one parsed Server-Sent Events frame: the event name and its
// (joined) data lines. It is the canonical Go-side SSE frame shape shared by the
// CLI (StreamJob) and the peer-http runner's stream mirroring (P2-a). The server
// emits these via httpapi.writeSSE; events are status / log / log-rotated /
// interaction / end (see internal/httpapi/stream_handler.go).
type SSEEvent struct {
	// Event is the SSE `event:` field (e.g. "status", "log", "end").
	Event string
	// Data is the (newline-joined) `data:` payload, typically a JSON object.
	Data []byte
}

// ParseSSE consumes complete frames from buffer (split on the blank-line frame
// separator "\n\n") and returns the parsed frames plus the trailing incomplete
// remainder, which the caller carries into the next read. It mirrors the
// frontend parser (web/src/api/sse.ts): \r\n is normalised, `event:` / `data:`
// lines are recognised and a single leading space after `data:` is stripped;
// multiple data lines are joined with "\n". Frames without an `event:` line are
// skipped (e.g. the opening ": open" comment).
func ParseSSE(buffer string) (frames []SSEEvent, rest string) {
	normalized := strings.ReplaceAll(buffer, "\r\n", "\n")
	parts := strings.Split(normalized, "\n\n")
	// The last element is the unterminated remainder.
	rest = parts[len(parts)-1]
	parts = parts[:len(parts)-1]

	for _, block := range parts {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var event string
		var dataLines []string
		haveEvent := false
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				haveEvent = true
			case strings.HasPrefix(line, "data:"):
				d := strings.TrimPrefix(line, "data:")
				d = strings.TrimPrefix(d, " ") // SSE: strip one leading space
				dataLines = append(dataLines, d)
			}
			// Other lines (id:, comment ":") are ignored.
		}
		if !haveEvent {
			continue
		}
		frames = append(frames, SSEEvent{Event: event, Data: []byte(strings.Join(dataLines, "\n"))})
	}
	return frames, rest
}
