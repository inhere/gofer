package peerhttp

import "strings"

// sseFrame is one parsed SSE frame: the event name and its (joined) data lines.
type sseFrame struct {
	Event string
	Data  string
}

// parseSSE consumes complete frames from buffer (split on the blank-line frame
// separator "\n\n") and returns the parsed frames plus the trailing incomplete
// remainder, which the caller carries into the next read. Mirrors the frontend
// parser (web/src/api/sse.ts): \r\n is normalised, `event:` / `data:` lines are
// recognised and a single leading space after `data:` is stripped; multiple
// data lines are joined with "\n".
func parseSSE(buffer string) (frames []sseFrame, rest string) {
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
		frames = append(frames, sseFrame{Event: event, Data: strings.Join(dataLines, "\n")})
	}
	return frames, rest
}
