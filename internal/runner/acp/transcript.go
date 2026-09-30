package acp

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	acpproto "github.com/inhere/gofer/internal/acp"
)

const (
	// maxEventLineBytes caps one acp.jsonl line. Message records are split at
	// maxMessageBytes before encoding; the larger JSON budget preserves their exact
	// text even when every byte needs JSON escaping.
	maxEventLineBytes = 8 << 20
	maxRawBytes       = 512
	maxPromptRunes    = 8000
	maxMessageBytes   = 1 << 20
)

// messageFlushInterval is the idle boundary between assistant message records.
// It is a variable so the runner contract test can shorten the wait without making
// production turns flush more aggressively.
var messageFlushInterval = 2 * time.Second

// addMessage appends an assistant shard to the current structured message. It never
// changes stdout: stdout is written independently by writeStdout. A very large block
// is split into consecutive message records at a UTF-8 boundary so acp.jsonl retains
// every byte without an unbounded in-memory builder.
func (h *handler) addMessage(text string) {
	if text == "" {
		return
	}
	h.mu.Lock()
	h.cancelMessageTimerLocked()
	for len(text) > 0 {
		room := maxMessageBytes - h.message.Len()
		if room == 0 {
			h.writeMessageLocked()
			room = maxMessageBytes
		}
		take := len(text)
		if take > room {
			take = utf8PrefixLen(text, room)
			if take == 0 {
				h.writeMessageLocked()
				continue
			}
		}
		h.message.WriteString(text[:take])
		text = text[take:]
		if h.message.Len() == maxMessageBytes {
			h.writeMessageLocked()
		}
	}
	if h.message.Len() > 0 {
		h.armMessageTimerLocked()
	}
	h.mu.Unlock()
}

// flushMessage persists the current assistant block synchronously. Keeping the
// eventWriter call under h.mu orders a timer callback before a concurrent stop flush.
func (h *handler) flushMessage() {
	h.mu.Lock()
	h.cancelMessageTimerLocked()
	h.writeMessageLocked()
	h.mu.Unlock()
}

func (h *handler) writeMessageLocked() {
	if h.message.Len() == 0 {
		return
	}
	text := h.message.String()
	h.message.Reset()
	h.events.write(map[string]any{"t": "message", "text": text})
}

func (h *handler) cancelMessageTimerLocked() {
	h.messageGeneration++
	if h.messageTimer != nil {
		h.messageTimer.Stop()
		h.messageTimer = nil
	}
}

func (h *handler) armMessageTimerLocked() {
	h.messageGeneration++
	generation := h.messageGeneration
	h.messageTimer = time.AfterFunc(messageFlushInterval, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if generation != h.messageGeneration {
			return
		}
		h.messageTimer = nil
		h.writeMessageLocked()
	})
}

func utf8PrefixLen(text string, maxBytes int) int {
	if len(text) <= maxBytes {
		return len(text)
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(text[:cut]) {
		cut--
	}
	return cut
}

// chunkText returns a content block's text (nil-safe).
func chunkText(c *acpproto.ContentBlock) string {
	if c == nil {
		return ""
	}
	return c.Text
}

// promptEvent records the user's turn input without allowing one prompt to make the
// structured log unbounded. The 8000-character contract counts Unicode code points,
// not UTF-8 bytes.
func promptEvent(text string) map[string]any {
	event := map[string]any{"t": "prompt", "text": text}
	runes := []rune(text)
	if len(runes) > maxPromptRunes {
		event["text"] = string(runes[:maxPromptRunes])
		event["truncated"] = true
	}
	return event
}

func toolCallEvent(tc *acpproto.ToolCall) map[string]any {
	event := map[string]any{"t": "tool_call", "tool_call_id": tc.ToolCallID}
	if tc.Title != "" {
		event["title"] = tc.Title
	}
	if tc.Kind != "" {
		event["kind"] = tc.Kind
	}
	if tc.Status != "" {
		event["status"] = tc.Status
	}
	if len(tc.Locations) > 0 {
		event["locations"] = append([]acpproto.ToolCallLocation(nil), tc.Locations...)
	}
	if len(tc.RawInput) > 0 {
		event["raw_input"] = truncate(string(tc.RawInput))
	}
	if len(tc.RawOutput) > 0 {
		event["raw_output"] = truncate(string(tc.RawOutput))
	}
	return event
}

func truncate(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxRawBytes {
		return text
	}
	return text[:maxRawBytes] + "…(truncated)"
}

// eventWriter appends JSON lines to acp.jsonl. A nil writer drops events; writes are
// serialised because notification handling and permission answers use different
// goroutines.
type eventWriter struct {
	mu sync.Mutex
	f  *os.File
}

func openEventWriter(resultDir string, appendExisting bool) (*eventWriter, error) {
	if resultDir == "" {
		return &eventWriter{}, nil
	}
	dir := filepath.Join(resultDir, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &eventWriter{}, err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendExisting {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(filepath.Join(dir, ACPFileName), flags, 0o644)
	if err != nil {
		return &eventWriter{}, err
	}
	return &eventWriter{f: f}, nil
}

func (w *eventWriter) write(event map[string]any) {
	if w == nil || w.f == nil {
		return
	}
	b, err := json.Marshal(event)
	if err != nil {
		slog.Debug("acp runner: encode event", "err", err)
		return
	}
	if len(b) > maxEventLineBytes {
		kind, _ := event["t"].(string)
		b, err = json.Marshal(map[string]any{"t": kind, "truncated": len(b)})
		if err != nil {
			return
		}
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(b); err != nil {
		slog.Debug("acp runner: write event", "err", err)
	}
}

func (w *eventWriter) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	return f.Close()
}
