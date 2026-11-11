package ndjsonfilter

import (
	"encoding/json"
	"io"
	"sync"
	"unicode/utf8"
)

// Per-turn transcript (gofer-mmh0): the capture also writes one JSON line per agent
// turn into the job's turns.jsonl, so a supervisor can read a long job turn by turn
// instead of getting only the last reply. The line type and its writer are defined
// here once; the job layer opens the file and the reader (job transcript) decodes
// the same TurnRecord.

// Turn dialects: which accumulator reads the stream (agent.NDJSONTurnsFor).
const (
	TurnsClaude = "claude"
	TurnsSuga   = "suga"
)

const (
	// MaxTurnTextBytes caps one turn's text in the file (cut at a UTF-8 boundary,
	// Truncated set).
	MaxTurnTextBytes = 256 << 10
	// MaxTurnsFileBytes caps turns.jsonl: past it further turns are dropped.
	MaxTurnsFileBytes = 8 << 20
	// turnBufferBytes bounds the in-memory text of the turn being accumulated; past
	// it the middle is dropped (head and tail kept), see turnText.
	turnBufferBytes = 1 << 20
	// turnBufferKeep is what one compaction keeps of each end; compacting down to
	// 2*keep (< turnBufferBytes) leaves headroom so it does not run on every delta.
	turnBufferKeep = 384 << 10
	turnGapMarker  = "\n…(omitted)…\n"
)

// TurnRecord is one line of turns.jsonl.
type TurnRecord struct {
	Turn int    `json:"turn"`
	Role string `json:"role"`
	Text string `json:"text"`
	// Tools counts the tool calls of the turn by tool name.
	Tools map[string]int `json:"tools,omitempty"`
	// Tokens is the turn's total token count when the stream reported one.
	Tokens int64 `json:"tokens,omitempty"`
	// Error is the error the stream reported during the turn.
	Error     string `json:"error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// TurnWriter encodes TurnRecords into turns.jsonl. Text and Error are passed
// through Mask BEFORE they are cut and JSON-encoded (an escaped secret would
// otherwise slip past a mask applied to the encoded bytes), then the text is cut to
// MaxTurnTextBytes. Safe for concurrent use.
type TurnWriter struct {
	mu      sync.Mutex
	w       io.Writer
	mask    func(string) string
	written int64
}

// NewTurnWriter wraps w. mask may be nil (no secrets granted).
func NewTurnWriter(w io.Writer, mask func(string) string) *TurnWriter {
	return &TurnWriter{w: w, mask: mask}
}

// Mask applies the writer's mask (identity when none).
func (t *TurnWriter) Mask(s string) string {
	if t == nil || t.mask == nil || s == "" {
		return s
	}
	return t.mask(s)
}

// WriteTurn masks, cuts and appends one record. Records past the file cap are
// dropped silently: the transcript is a best-effort sidecar and must never fail a job.
func (t *TurnWriter) WriteTurn(rec TurnRecord) error {
	if t == nil || t.w == nil {
		return nil
	}
	rec.Text = t.Mask(rec.Text)
	rec.Error = t.Mask(rec.Error)
	if len(rec.Text) > MaxTurnTextBytes {
		rec.Text = rec.Text[:cutRuneBoundary(rec.Text, MaxTurnTextBytes)]
		rec.Truncated = true
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.written+int64(len(b)) > MaxTurnsFileBytes {
		return nil
	}
	n, err := t.w.Write(b)
	t.written += int64(n)
	return err
}

// cutRuneBoundary returns the largest index <= n that does not split a UTF-8 rune.
func cutRuneBoundary(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// turnText accumulates one turn's text in memory with a hard bound: beyond
// turnBufferBytes the buffer is masked as a whole (so a value straddling the part
// about to be dropped is already replaced) and its middle is dropped.
type turnText struct {
	w         *TurnWriter
	buf       string
	truncated bool
}

func (t *turnText) add(s string) {
	t.buf += s
	if len(t.buf) <= turnBufferBytes {
		return
	}
	t.buf = t.w.Mask(t.buf)
	if len(t.buf) <= turnBufferBytes {
		return
	}
	head := cutRuneBoundary(t.buf, turnBufferKeep)
	tail := len(t.buf) - turnBufferKeep
	for tail < len(t.buf) && !utf8.RuneStart(t.buf[tail]) {
		tail++
	}
	t.buf = t.buf[:head] + turnGapMarker + t.buf[tail:]
	t.truncated = true
}

func (t *turnText) len() int { return len(t.buf) }

// cut drops everything after length n (a suga restart of the latest segment).
func (t *turnText) cut(n int) {
	if n < len(t.buf) {
		t.buf = t.buf[:n]
	}
}

func (t *turnText) reset() { t.buf, t.truncated = "", false }

// turnObserver reads parsed stream rows (before the keep whitelist) and writes
// finished turns.
type turnObserver interface {
	observe(typ string, obj map[string]any)
	finish() // Close: write a turn that was still open
}

func newTurnObserver(dialect string, w *TurnWriter) turnObserver {
	switch dialect {
	case TurnsClaude:
		return &claudeTurns{w: w, text: turnText{w: w}}
	case TurnsSuga:
		return &sugaTurns{w: w, text: turnText{w: w}}
	}
	return nil
}

// claudeTurns: one `claude -p` run is one turn; its assistant text blocks are joined
// and the `result` row closes it. A run killed before `result` is written at Close.
type claudeTurns struct {
	w       *TurnWriter
	text    turnText
	tools   map[string]int
	toolIDs map[string]struct{}
	open    bool
	turn    int
}

func (c *claudeTurns) observe(typ string, obj map[string]any) {
	switch typ {
	case "assistant":
		if stringField(obj, "parent_tool_use_id") != "" {
			return // a sub-agent's chatter is not the main narrative
		}
		blocks := contentBlocks(obj)
		if t := textFromBlocks(blocks); t != "" {
			c.open = true
			if c.text.len() > 0 {
				c.text.add("\n\n")
			}
			c.text.add(t)
		}
		for _, b := range blocks {
			m, ok := b.(map[string]any)
			if !ok || stringField(m, "type") != "tool_use" {
				continue
			}
			if id := stringField(m, "id"); id != "" {
				if _, dup := c.toolIDs[id]; dup {
					continue
				}
				if c.toolIDs == nil {
					c.toolIDs = map[string]struct{}{}
				}
				c.toolIDs[id] = struct{}{}
			}
			c.open = true
			if c.tools == nil {
				c.tools = map[string]int{}
			}
			c.tools[stringField(m, "name")]++
		}
	case "result":
		var errText string
		if v, _ := obj["is_error"].(bool); v {
			errText = stringField(obj, "result")
			if errText == "" {
				errText = stringField(obj, "subtype")
			}
		}
		if c.text.len() == 0 {
			c.text.add(stringField(obj, "result"))
		}
		var tokens int64
		if u := claudeUsage(obj); u != nil {
			tokens = totalTokens(u.TotalTokens, u.InputTokens, u.OutputTokens)
		}
		c.open = true
		c.flush(tokens, errText)
	}
}

func (c *claudeTurns) flush(tokens int64, errText string) {
	if !c.open {
		return
	}
	c.turn++
	_ = c.w.WriteTurn(TurnRecord{Turn: c.turn, Role: "assistant", Text: c.text.buf, Tools: c.tools, Tokens: tokens, Error: errText, Truncated: c.text.truncated})
	c.text.reset()
	c.tools, c.toolIDs, c.open = nil, nil, false
}

func (c *claudeTurns) finish() { c.flush(0, "") }

// sugaTurns: `turn_start` opens a turn, `message_delta` rows accumulate its text,
// `tool_call` counts tools, `turn_end` (or the following `result`) closes it.
// `retry{restart:true}` means the model call is re-sent from scratch, so only the
// text accumulated since the latest tool_result / turn_start is dropped.
type sugaTurns struct {
	w        *TurnWriter
	text     turnText
	tools    map[string]int
	open     bool
	ended    bool // turn_end seen, waiting only for `result` to fill an empty text
	turn     int
	segStart int // len(text) at the latest turn_start / tool_result
	tokens   int64
	errText  string
}

func (s *sugaTurns) observe(typ string, obj map[string]any) {
	switch typ {
	case "turn_start":
		s.flush()
		s.open = true
	case "message_delta":
		s.ensureOpen()
		s.text.add(stringField(obj, "text"))
	case "tool_call":
		s.ensureOpen()
		if s.tools == nil {
			s.tools = map[string]int{}
		}
		s.tools[stringField(obj, "name")]++
	case "tool_result":
		s.segStart = s.text.len()
	case "retry":
		if v, _ := obj["restart"].(bool); v && s.open {
			s.text.cut(s.segStart)
		}
	case "error":
		s.ensureOpen()
		s.errText = stringField(obj, "error")
	case "turn_end":
		s.ensureOpen()
		if u := usageAt(obj, "", "usage"); u != nil {
			s.tokens = totalTokens(u.TotalTokens, u.InputTokens, u.OutputTokens)
		}
		s.ended = true
		if s.text.len() > 0 {
			s.flush()
		}
	case "result":
		if !s.open {
			return
		}
		if s.text.len() == 0 {
			s.text.add(stringField(obj, "result"))
		}
		if s.tokens == 0 {
			if u := usageAt(obj, "", "usage"); u != nil {
				s.tokens = totalTokens(u.TotalTokens, u.InputTokens, u.OutputTokens)
			}
		}
		s.flush()
	}
}

func (s *sugaTurns) ensureOpen() {
	if !s.open {
		s.open = true
		s.segStart = 0
	}
}

func (s *sugaTurns) flush() {
	if !s.open {
		return
	}
	s.turn++
	_ = s.w.WriteTurn(TurnRecord{Turn: s.turn, Role: "assistant", Text: s.text.buf, Tools: s.tools, Tokens: s.tokens, Error: s.errText, Truncated: s.text.truncated})
	s.text.reset()
	s.tools, s.open, s.ended, s.segStart, s.tokens, s.errText = nil, false, false, 0, 0, ""
}

func (s *sugaTurns) finish() { s.flush() }

func totalTokens(total, in, out int64) int64 {
	if total > 0 {
		return total
	}
	return in + out
}
