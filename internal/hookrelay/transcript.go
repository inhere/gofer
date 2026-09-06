package hookrelay

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// transcriptTail is how much of the transcript's end is scanned for the last
// assistant message. Claude Code transcripts are JSONL appended per event; the
// last assistant text is within the final few entries, but a large tool result
// can sit between them, hence a generous window.
const transcriptTail = 512 * 1024

// LastAssistantText returns the text of the last assistant message in a Claude
// Code transcript (JSONL), or "" when none is found. The entry shape is
// internal to Claude Code and may change between releases (design TBD-4), so
// the scan is deliberately tolerant: it accepts `type=="assistant"` entries
// whose `message.content` is either a string or an array of `{type:"text",
// text}` blocks, and also entries whose `message.role=="assistant"`. Text
// blocks of one message are joined by blank lines. maxBytes > 0 truncates the
// result.
func LastAssistantText(path string, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	var start int64
	if st.Size() > transcriptTail {
		start = st.Size() - transcriptTail
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if start > 0 {
		// Drop the partial first line of the window.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if text, ok := assistantTextOf(line); ok {
			return truncate(text, maxBytes), nil
		}
	}
	return "", nil
}

// transcriptEntry is the subset of a Claude Code transcript line we look at.
type transcriptEntry struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// assistantTextOf extracts assistant text from one JSONL entry. ok is false
// when the entry is not an assistant message or carries no text (e.g. a pure
// tool_use block — the hook then keeps scanning backwards).
func assistantTextOf(line []byte) (string, bool) {
	var e transcriptEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return "", false
	}
	if e.Type != "assistant" && e.Message.Role != "assistant" {
		return "", false
	}
	if len(e.Message.Content) == 0 {
		return "", false
	}
	var asString string
	if json.Unmarshal(e.Message.Content, &asString) == nil {
		asString = strings.TrimSpace(asString)
		return asString, asString != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n\n"), true
}

// truncate cuts s to at most n bytes (n <= 0 = no limit) on a rune boundary.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}
