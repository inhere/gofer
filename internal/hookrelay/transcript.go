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
	lines, err := transcriptTailLines(path, transcriptTail)
	if err != nil {
		return "", err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if text, ok := assistantTextOf(lines[i]); ok {
			return truncate(text, maxBytes), nil
		}
	}
	return "", nil
}

// transcriptTailLines reads the last limit bytes of a JSONL transcript and returns
// its complete, non-empty JSON-object lines (the partial first line of the
// window is dropped).
func transcriptTailLines(path string, limit int64) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var start int64
	if st.Size() > limit {
		start = st.Size() - limit
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	var out [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if line = bytes.TrimSpace(line); len(line) > 0 && line[0] == '{' {
			out = append(out, line)
		}
	}
	return out, nil
}

// permissionDenialTail bounds the scan for a terminal denial: the rejected
// tool_result is written right after the tool_use it answers.
const permissionDenialTail = 256 * 1024

// TerminalDenied reports whether a Claude Code transcript shows that the person
// answered the permission prompt of this tool call with No / Esc in the terminal:
// a tool_result with is_error for a tool_use whose name + input fingerprint match,
// marked as a user rejection (Claude Code 2.1.295 writes `toolDenialKind:
// "user-rejected"` / `toolUseResult: "User rejected tool use"` on that entry,
// ~10ms after the key press). Tolerant: any read / shape problem is "false".
func TerminalDenied(path, tool string, input json.RawMessage) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	lines, err := transcriptTailLines(path, permissionDenialTail)
	if err != nil {
		return false
	}
	fp := PermissionFingerprint(tool, input)
	calls := map[string]bool{} // tool_use ids of this call
	for _, line := range lines {
		var e struct {
			Type           string          `json:"type"`
			ToolUseResult  json.RawMessage `json:"toolUseResult"`
			ToolDenialKind string          `json:"toolDenialKind"`
			Message        struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
		}
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		rejected := e.ToolDenialKind == "user-rejected" ||
			strings.Contains(string(e.ToolUseResult), "User rejected tool use")
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use" && b.Name == tool && PermissionFingerprint(b.Name, b.Input) == fp:
				calls[b.ID] = true
			case b.Type == "tool_result" && b.IsError && rejected && calls[b.ToolUseID]:
				return true
			}
		}
	}
	return false
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
	const marker = "\n[已截断]"
	limit := n - len(marker)
	if limit <= 0 {
		cut := s[:n]
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		return cut + "…"
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + marker
}
