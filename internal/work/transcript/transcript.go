// Package transcript turns an agent CLI's own session file into a short list of
// conversation turns for the work-item summarizer (W2a, design §14.2). It reads only
// the tail of a file the session registered, parses the three jsonl dialects gofer
// knows (claude, codex rollout, omp), and never touches the network or any tool.
//
// Parsing is deliberately forgiving: an unknown line, a missing field or a truncated
// first line is skipped, because a transcript is a moving target written by someone
// else's program.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Turn roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool" // a one-line note that the agent ran a tool
)

// Turn is one utterance (or tool note) of the conversation.
type Turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Dialects.
const (
	DialectClaude = "claude"
	DialectCodex  = "codex"
	DialectOmp    = "omp"
)

// DialectFor maps a gofer agent name (or an acp variant of it) to the transcript
// dialect, "" when unknown (Parse then sniffs the content).
func DialectFor(agent string) string {
	a := strings.ToLower(strings.TrimSpace(agent))
	a = strings.TrimSuffix(a, "-acp")
	a = strings.TrimSuffix(a, ".exe")
	switch {
	case strings.HasPrefix(a, "claude"):
		return DialectClaude
	case strings.HasPrefix(a, "codex"):
		return DialectCodex
	case strings.HasPrefix(a, "omp"):
		return DialectOmp
	}
	return ""
}

// ReadTail reads at most maxBytes from the end of the file. When it had to cut, the
// first (partial) line is dropped so the result starts on a line boundary.
func ReadTail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("transcript: %s is a directory", path)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultTailBytes
	}
	size := fi.Size()
	start := int64(0)
	if size > maxBytes {
		start = size - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil
		}
	}
	return buf, nil
}

// DefaultTailBytes is how much of a transcript's end is read by default.
const DefaultTailBytes = 256 * 1024

// Parse extracts the conversation from raw jsonl. dialect "" sniffs it from the lines.
func Parse(dialect string, raw []byte) []Turn {
	var turns []Turn
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var o map[string]json.RawMessage
		if json.Unmarshal(line, &o) != nil {
			continue
		}
		d := dialect
		if d == "" {
			d = sniff(o)
		}
		switch d {
		case DialectClaude:
			turns = append(turns, parseClaude(o)...)
		case DialectCodex:
			turns = append(turns, parseCodex(o)...)
		case DialectOmp:
			turns = append(turns, parseOmp(o)...)
		}
	}
	return mergeAdjacent(turns)
}

func sniff(o map[string]json.RawMessage) string {
	switch str(o["type"]) {
	case "response_item", "event_msg", "session_meta", "turn_context":
		return DialectCodex
	case "user", "assistant":
		if _, ok := o["message"]; ok {
			return DialectClaude
		}
	case "message", "session":
		return DialectOmp
	}
	return ""
}

func str(r json.RawMessage) string {
	var s string
	if len(r) > 0 && json.Unmarshal(r, &s) == nil {
		return s
	}
	return ""
}

// blocks decodes a message "content" that is either a string or a block list.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Arguments json.RawMessage `json:"arguments"`
}

func contentBlocks(raw json.RawMessage) []block {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []block{{Type: "text", Text: s}}
	}
	var bs []block
	if json.Unmarshal(raw, &bs) == nil {
		return bs
	}
	return nil
}

// toolNote renders a tool call as one short line.
func toolNote(name string, args json.RawMessage) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	detail := ""
	var m map[string]any
	if len(args) > 0 && json.Unmarshal(args, &m) == nil {
		for _, k := range []string{"command", "cmd", "file_path", "path", "pattern", "url", "description", "query", "code"} {
			if v, ok := m[k]; ok {
				detail = fmt.Sprint(v)
				break
			}
		}
	} else if s := str(args); s != "" {
		detail = s
	}
	detail = oneLine(detail, 80)
	if detail == "" {
		return "[tool " + name + "]"
	}
	return "[tool " + name + ": " + detail + "]"
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// injected reports text a harness (not the person) put into the user turn.
func injected(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range []string{"<system-reminder>", "<environment_context>", "<local-command", "<command-name>", "<command-message>",
		"Caveat: The messages below were generated", "# AGENTS.md instructions", "<user-instructions>", "<INSTRUCTIONS>"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

func textTurns(role string, bs []block) []Turn {
	var out []Turn
	var sb strings.Builder
	flush := func() {
		if t := strings.TrimSpace(sb.String()); t != "" {
			out = append(out, Turn{Role: role, Text: t})
		}
		sb.Reset()
	}
	for _, b := range bs {
		switch b.Type {
		case "text", "input_text", "output_text":
			if role == RoleUser && injected(b.Text) {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		case "tool_use", "toolCall", "tool_call", "function_call":
			flush()
			args := b.Input
			if len(args) == 0 {
				args = b.Arguments
			}
			out = append(out, Turn{Role: RoleTool, Text: toolNote(b.Name, args)})
		}
	}
	flush()
	return out
}

func parseClaude(o map[string]json.RawMessage) []Turn {
	t := str(o["type"])
	if t != "user" && t != "assistant" {
		return nil
	}
	if string(o["isMeta"]) == "true" {
		return nil
	}
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(o["message"], &msg) != nil {
		return nil
	}
	role := RoleUser
	if t == "assistant" {
		role = RoleAssistant
	}
	return textTurns(role, contentBlocks(msg.Content))
}

func parseCodex(o map[string]json.RawMessage) []Turn {
	switch str(o["type"]) {
	case "response_item":
		var p struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(o["payload"], &p) != nil {
			return nil
		}
		switch p.Type {
		case "message":
			switch p.Role {
			case "user":
				return textTurns(RoleUser, contentBlocks(p.Content))
			case "assistant":
				return textTurns(RoleAssistant, contentBlocks(p.Content))
			}
		case "function_call", "custom_tool_call", "local_shell_call":
			return []Turn{{Role: RoleTool, Text: toolNote(p.Name, p.Arguments)}}
		}
	}
	return nil
}

func parseOmp(o map[string]json.RawMessage) []Turn {
	if str(o["type"]) != "message" {
		return nil
	}
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		// Attribution says who authored a user-role message: "user" is the person,
		// "agent" is something the harness injected on its behalf (real omp sessions
		// also record async job results as custom_message entries with attribution
		// "agent", which this parser never reads).
		Attribution string `json:"attribution"`
	}
	if json.Unmarshal(o["message"], &msg) != nil {
		return nil
	}
	switch msg.Role {
	case "user":
		if msg.Attribution == "agent" {
			return nil
		}
		return textTurns(RoleUser, contentBlocks(msg.Content))
	case "assistant":
		return textTurns(RoleAssistant, contentBlocks(msg.Content))
	}
	return nil
}

// mergeAdjacent folds consecutive tool notes into one turn so a long tool burst does
// not eat the budget.
func mergeAdjacent(in []Turn) []Turn {
	out := make([]Turn, 0, len(in))
	for _, t := range in {
		if n := len(out); n > 0 && t.Role == RoleTool && out[n-1].Role == RoleTool {
			out[n-1].Text += " " + t.Text
			continue
		}
		out = append(out, t)
	}
	return out
}

// FormatOpts bounds the rendered tail.
type FormatOpts struct {
	// MaxTurns keeps at most this many of the newest turns (default 40).
	MaxTurns int
	// MaxBytes bounds the whole rendering (default 32KB); older turns are dropped first.
	MaxBytes int
	// MaxTurnRunes caps one turn's text (default 2000); an over-long turn keeps its head
	// and its tail (the conclusion is usually at the end).
	MaxTurnRunes int
}

func (o FormatOpts) withDefaults() FormatOpts {
	if o.MaxTurns <= 0 {
		o.MaxTurns = 40
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 32 * 1024
	}
	if o.MaxTurnRunes <= 0 {
		o.MaxTurnRunes = 2000
	}
	return o
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	head := max * 2 / 5
	tail := max - head
	return string(r[:head]) + "\n…（中间省略）…\n" + string(r[len(r)-tail:])
}

// Format renders the newest turns as plain text, oldest first, within the bounds.
func Format(turns []Turn, o FormatOpts) string {
	o = o.withDefaults()
	if len(turns) > o.MaxTurns {
		turns = turns[len(turns)-o.MaxTurns:]
	}
	labels := map[string]string{RoleUser: "用户", RoleAssistant: "助手", RoleTool: "工具"}
	parts := make([]string, 0, len(turns))
	for _, t := range turns {
		txt := t.Text
		if t.Role == RoleTool {
			txt = oneLine(txt, 400)
		} else {
			txt = clip(txt, o.MaxTurnRunes)
		}
		parts = append(parts, labels[t.Role]+"：\n"+txt)
	}
	// Drop the oldest until it fits.
	for len(parts) > 1 {
		total := 0
		for _, p := range parts {
			total += len(p) + 2
		}
		if total <= o.MaxBytes {
			break
		}
		parts = parts[1:]
	}
	return strings.Join(parts, "\n\n")
}
