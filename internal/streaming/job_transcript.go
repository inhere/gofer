package streaming

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/secret"
	"github.com/inhere/gofer/internal/store"
)

// Job transcript (gofer-mmh0): a long job read turn by turn. The source is, in order,
// the per-turn sidecar the capture wrote (turns.jsonl, claude / suga), the ACP
// structured record (artifacts/acp.jsonl, also mirrored from remote workers), and
// finally the whole stdout as one turn. Reading is paged: one response carries at most
// TranscriptPageBytes of text and a cursor {turn, offset} to continue, so any turn,
// however large, can be fetched completely. Nothing here writes a file.

const (
	// TranscriptPageBytes caps the text one response carries.
	TranscriptPageBytes = 64 << 10
	// TranscriptMaxSearch is the longest search term (a literal substring).
	TranscriptMaxSearch = 200
	// transcriptMaxTurnsArg bounds the turn numbers / counts a caller may name.
	transcriptMaxTurnsArg = 1_000_000
	// transcriptScanBytes bounds how much of one file a single read scans.
	transcriptScanBytes = 64 << 20
	// transcriptStdoutBytes bounds the stdout fallback (one turn).
	transcriptStdoutBytes = 16 << 20
	// transcriptTurnTextBytes bounds one ACP turn's assembled reply.
	transcriptTurnTextBytes = 8 << 20
	transcriptPreviewRunes  = 100
	transcriptSnippetBytes  = 80
	transcriptMaxSnippets   = 3
	// transcriptEntryOverhead approximates the fixed JSON cost of one turn entry.
	transcriptEntryOverhead = 256
	transcriptTurnsFileName = store.TurnsFile
)

// Read modes.
const (
	TranscriptOutline = "outline"
	TranscriptTurns   = "turns"
	TranscriptHead    = "head"
	TranscriptTail    = "tail"
	TranscriptSearch  = "search"
)

// Sources of a transcript.
const (
	TranscriptSourceTurns  = "turns"
	TranscriptSourceACP    = "acp"
	TranscriptSourceStdout = "stdout"
)

// ErrInvalidTranscript marks a bad parameter combination (an HTTP 400).
var ErrInvalidTranscript = errors.New("invalid transcript request")

// ErrTranscriptPath marks a result-dir file that resolves outside the job's directory.
var ErrTranscriptPath = errors.New("transcript path escapes the job's result directory")

// TranscriptQuery is the raw, string-typed request (what the HTTP query / MCP input
// carries). Exactly one of Outline / Turns / Head / Tail / Search may be set; none means
// outline.
type TranscriptQuery struct {
	Outline bool
	Turns   string // "A-B" (or "A" for one turn)
	Head    string
	Tail    string
	Search  string
	// Cursor is "turn:offset" from a previous page's Next.
	Cursor string
}

// TranscriptCursor continues a read: the turn to resume at and the byte offset inside
// that turn's text.
type TranscriptCursor struct {
	Turn   int `json:"turn"`
	Offset int `json:"offset"`
}

func (c TranscriptCursor) String() string { return fmt.Sprintf("%d:%d", c.Turn, c.Offset) }

// TranscriptOpts is a validated TranscriptQuery.
type TranscriptOpts struct {
	Mode     string
	From, To int // turns (From..To, 1-based, inclusive)
	N        int // head / tail
	Search   string
	Cursor   TranscriptCursor
}

func invalidTranscript(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidTranscript, fmt.Sprintf(format, args...))
}

// ParseTranscriptQuery validates q: the modes are mutually exclusive, ranges and counts
// are bounded, the search term is a non-empty literal of at most TranscriptMaxSearch
// runes.
func ParseTranscriptQuery(q TranscriptQuery) (TranscriptOpts, error) {
	var set []string
	if q.Outline {
		set = append(set, "outline")
	}
	if q.Turns != "" {
		set = append(set, "turns")
	}
	if q.Head != "" {
		set = append(set, "head")
	}
	if q.Tail != "" {
		set = append(set, "tail")
	}
	if q.Search != "" {
		set = append(set, "search")
	}
	if len(set) > 1 {
		return TranscriptOpts{}, invalidTranscript("%s are mutually exclusive", strings.Join(set, ", "))
	}
	opts := TranscriptOpts{Mode: TranscriptOutline}
	switch {
	case q.Turns != "":
		from, to, err := parseTurnRange(q.Turns)
		if err != nil {
			return TranscriptOpts{}, err
		}
		opts.Mode, opts.From, opts.To = TranscriptTurns, from, to
	case q.Head != "":
		n, err := parseTurnCount("head", q.Head)
		if err != nil {
			return TranscriptOpts{}, err
		}
		opts.Mode, opts.N = TranscriptHead, n
	case q.Tail != "":
		n, err := parseTurnCount("tail", q.Tail)
		if err != nil {
			return TranscriptOpts{}, err
		}
		opts.Mode, opts.N = TranscriptTail, n
	case q.Search != "":
		if strings.TrimSpace(q.Search) == "" {
			return TranscriptOpts{}, invalidTranscript("search must not be blank")
		}
		if utf8.RuneCountInString(q.Search) > TranscriptMaxSearch {
			return TranscriptOpts{}, invalidTranscript("search is longer than %d characters", TranscriptMaxSearch)
		}
		opts.Mode, opts.Search = TranscriptSearch, q.Search
	}
	if q.Cursor != "" {
		cur, err := parseTranscriptCursor(q.Cursor)
		if err != nil {
			return TranscriptOpts{}, err
		}
		opts.Cursor = cur
	}
	return opts, nil
}

func parseTurnCount(name, v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > transcriptMaxTurnsArg {
		return 0, invalidTranscript("%s must be an integer between 1 and %d", name, transcriptMaxTurnsArg)
	}
	return n, nil
}

func parseTurnRange(v string) (from, to int, err error) {
	lo, hi, isRange := strings.Cut(strings.TrimSpace(v), "-")
	from, err = strconv.Atoi(strings.TrimSpace(lo))
	if err == nil {
		to = from
		if isRange {
			to, err = strconv.Atoi(strings.TrimSpace(hi))
		}
	}
	if err != nil || from < 1 || to < from || to > transcriptMaxTurnsArg {
		return 0, 0, invalidTranscript("turns must be A or A-B with 1 <= A <= B <= %d", transcriptMaxTurnsArg)
	}
	return from, to, nil
}

func parseTranscriptCursor(v string) (TranscriptCursor, error) {
	t, o, ok := strings.Cut(v, ":")
	turn, err1 := strconv.Atoi(t)
	off, err2 := strconv.Atoi(o)
	if !ok || err1 != nil || err2 != nil || turn < 1 || off < 0 || turn > transcriptMaxTurnsArg {
		return TranscriptCursor{}, invalidTranscript("cursor must be <turn>:<offset> as returned by a previous page")
	}
	return TranscriptCursor{Turn: turn, Offset: off}, nil
}

// JobTranscriptTurn is one turn of a page. Which fields are set depends on the mode:
// outline sets Chars / Preview / Prompt (a preview), turns / head / tail set Text (and
// Prompt for an ACP turn), search sets Matches.
type JobTranscriptTurn struct {
	Turn   int    `json:"turn"`
	Role   string `json:"role,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Text   string `json:"text,omitempty"`
	// Offset is where Text starts inside the turn's text (non-zero on a continued page).
	Offset int `json:"offset,omitempty"`
	// Chars is the turn text's total size in bytes (after secret redaction; in the
	// outline, the stored size, since only the previewed head is redacted there).
	Chars   int            `json:"chars"`
	Preview string         `json:"preview,omitempty"`
	Tools   map[string]int `json:"tools,omitempty"`
	Tokens  int64          `json:"tokens,omitempty"`
	Error   string         `json:"error,omitempty"`
	// Truncated reports that the capture itself cut this turn's text.
	Truncated  bool     `json:"truncated,omitempty"`
	Matches    []string `json:"matches,omitempty"`
	MatchCount int      `json:"match_count,omitempty"`
}

// JobTranscriptPage is one response. Next, when set, is the cursor of the rest.
type JobTranscriptPage struct {
	Source    string              `json:"source"`
	Mode      string              `json:"mode"`
	Total     int                 `json:"total"`
	Turns     []JobTranscriptTurn `json:"turns"`
	Truncated bool                `json:"truncated,omitempty"`
	Next      *TranscriptCursor   `json:"next,omitempty"`
}

// rawTurn is one turn as read from a source, before redaction.
type rawTurn struct {
	Turn      int
	Role      string
	Prompt    string
	Text      string
	Tools     map[string]int
	Tokens    int64
	Error     string
	Truncated bool
}

// transcriptSource is where a job's turns live.
type transcriptSource struct {
	kind string
	path string
}

// locateTranscript picks the job's source. Every path is joined under the job's result
// dir with store.SafeJoinUnder (a symlink pointing outside is refused).
func locateTranscript(res job.JobResult) (transcriptSource, error) {
	if res.ResultDir == "" {
		return transcriptSource{}, nil
	}
	for _, c := range []struct{ kind, rel string }{
		{TranscriptSourceTurns, transcriptTurnsFileName},
		{TranscriptSourceACP, "artifacts/" + acpArtifactFile},
		{TranscriptSourceStdout, store.StdoutFile},
	} {
		p, err := store.SafeJoinUnder(res.ResultDir, c.rel)
		if err != nil {
			return transcriptSource{}, fmt.Errorf("%w: %v", ErrTranscriptPath, err)
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return transcriptSource{kind: c.kind, path: p}, nil
		}
	}
	return transcriptSource{}, nil
}

// TurnCount is the number of turns a job's transcript has, 0 when it has none. It only
// counts lines (bounded scan), so it is cheap enough for the job detail response.
func TurnCount(res job.JobResult) int {
	src, err := locateTranscript(res)
	if err != nil || src.kind == "" {
		return 0
	}
	return countTurns(src)
}

func countTurns(src transcriptSource) int {
	switch src.kind {
	case TranscriptSourceStdout:
		if fi, err := os.Stat(src.path); err == nil && fi.Size() > 0 {
			return 1
		}
		return 0
	case TranscriptSourceTurns, TranscriptSourceACP:
		f, err := os.Open(src.path)
		if err != nil {
			return 0
		}
		defer f.Close()
		n := 0
		_ = scanLines(f, func(line []byte) bool {
			switch {
			case src.kind == TranscriptSourceTurns:
				n++
			case bytes.HasPrefix(line, acpPromptPrefix):
				n++
			case n == 0 && bytes.HasPrefix(line, acpMessagePrefix):
				n++ // a reply with no prompt before it still forms a turn (see readTurns)
			}
			return true
		})
		return n
	}
	return 0
}

// acp.jsonl lines are encoded from maps, so keys are sorted and `t` leads every record
// that has no key sorting before it (prompt / message records have only t, text,
// truncated): a prefix check identifies them without decoding.
var (
	acpPromptPrefix  = []byte(`{"t":"prompt"`)
	acpMessagePrefix = []byte(`{"t":"message"`)
)

// scanLines feeds each non-empty line of r to fn until fn returns false or
// transcriptScanBytes were read. A line is never longer than the writer's own cap (8MB)
// plus slack; longer ones are skipped.
func scanLines(r io.Reader, fn func(line []byte) bool) error {
	br := bufio.NewReaderSize(io.LimitReader(r, transcriptScanBytes), 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if l := bytes.TrimSpace(line); len(l) > 0 && len(l) <= 9<<20 {
				if !fn(l) {
					return nil
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// readTurns yields the source's turns in order; fn returns false to stop early.
func readTurns(src transcriptSource, fn func(rawTurn) bool) error {
	switch src.kind {
	case TranscriptSourceStdout:
		b, err := readCapped(src.path, transcriptStdoutBytes)
		if err != nil || len(b) == 0 {
			return err
		}
		fn(rawTurn{Turn: 1, Role: "assistant", Text: string(b)})
		return nil
	case TranscriptSourceTurns:
		f, err := os.Open(src.path)
		if err != nil {
			return err
		}
		defer f.Close()
		return scanLines(f, func(line []byte) bool {
			var rec ndjsonfilter.TurnRecord
			if json.Unmarshal(line, &rec) != nil {
				return true
			}
			return fn(rawTurn{Turn: rec.Turn, Role: rec.Role, Text: rec.Text, Tools: rec.Tools, Tokens: rec.Tokens, Error: rec.Error, Truncated: rec.Truncated})
		})
	case TranscriptSourceACP:
		f, err := os.Open(src.path)
		if err != nil {
			return err
		}
		defer f.Close()
		return readACPTurns(f, fn)
	}
	return nil
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// readACPTurns cuts acp.jsonl into turns at its `prompt` records (the same rule
// ReadTranscript uses): the turn is the prompt plus the assistant `message` blocks (joined
// by a blank line) and the tool calls that follow it. Tools are counted once per
// tool_call_id, keyed by the tool's kind (title when it has none).
func readACPTurns(r io.Reader, fn func(rawTurn) bool) error {
	var cur *rawTurn
	seen := map[string]struct{}{}
	n := 0
	flush := func() bool {
		if cur == nil {
			return true
		}
		t := *cur
		cur = nil
		clear(seen)
		return fn(t)
	}
	open := func() {
		n++
		cur = &rawTurn{Turn: n, Role: "assistant"}
	}
	stopped := false
	err := scanLines(r, func(line []byte) bool {
		var rec map[string]any
		if json.Unmarshal(line, &rec) != nil {
			return true
		}
		switch kind, _ := rec["t"].(string); kind {
		case "prompt":
			if !flush() {
				stopped = true
				return false
			}
			open()
			cur.Prompt = stringValue(rec["text"])
		case "message":
			if cur == nil {
				open()
			}
			if cur.Text != "" {
				cur.Text += "\n\n"
			}
			if room := transcriptTurnTextBytes - len(cur.Text); room > 0 {
				txt := stringValue(rec["text"])
				if len(txt) > room {
					txt = txt[:cutBoundary(txt, room)]
					cur.Truncated = true
				}
				cur.Text += txt
			} else {
				cur.Truncated = true
			}
		case "tool_call":
			if cur == nil {
				open()
			}
			id := stringValue(rec["tool_call_id"])
			if _, dup := seen[id]; dup && id != "" {
				return true
			}
			seen[id] = struct{}{}
			name := stringValue(rec["kind"])
			if name == "" || name == "null" {
				name = stringValue(rec["title"])
			}
			if name == "" || name == "null" {
				name = "tool"
			}
			if cur.Tools == nil {
				cur.Tools = map[string]int{}
			}
			cur.Tools[name]++
		}
		return true
	})
	if err != nil || stopped {
		return err
	}
	flush()
	return nil
}

func cutBoundary(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// redactTurnText applies the read-side redaction (the same as the job messages page):
// credential-looking assignments become a placeholder.
func redactTurnText(s string) string {
	out, _ := secret.RedactString(s)
	return out
}

// promptBody removes the injected-rules frame from a prompt.
func promptBody(p string) string {
	body, _ := job.SplitInjectedRules(p)
	return body
}

// prepare is a rawTurn after redaction (and the injected-rules frame removed from
// a prompt).
func prepare(t rawTurn) rawTurn {
	t.Prompt = redactTurnText(promptBody(t.Prompt))
	t.Text = redactTurnText(t.Text)
	t.Error = redactTurnText(t.Error)
	return t
}

// JobTranscript reads one page of the job's transcript. The job's location comes from
// its stored result (never from the caller).
func JobTranscript(res job.JobResult, opts TranscriptOpts) (JobTranscriptPage, error) {
	if opts.Mode == "" {
		opts.Mode = TranscriptOutline
	}
	src, err := locateTranscript(res)
	if err != nil {
		return JobTranscriptPage{}, err
	}
	page := JobTranscriptPage{Source: src.kind, Mode: opts.Mode, Turns: []JobTranscriptTurn{}}
	if src.kind == "" {
		return page, nil
	}
	page.Total = countTurns(src)
	if page.Total == 0 {
		return page, nil
	}
	cur := opts.Cursor
	if cur.Turn < 1 {
		cur = TranscriptCursor{Turn: 1}
	}
	switch opts.Mode {
	case TranscriptOutline:
		err = readOutline(src, cur, &page)
	case TranscriptSearch:
		err = readSearch(src, opts.Search, cur, &page)
	default:
		from, to := opts.From, opts.To
		switch opts.Mode {
		case TranscriptHead:
			from, to = 1, opts.N
		case TranscriptTail:
			from, to = max(1, page.Total-opts.N+1), page.Total
		}
		if from < 1 {
			from = 1
		}
		to = min(to, page.Total)
		err = readRange(src, from, to, cur, &page)
	}
	return page, err
}

// previewHead redacts and shortens the head of s for an outline line.
func previewHead(s string) string {
	const window = 2048
	if len(s) > window {
		s = s[:cutBoundary(s, window)]
	}
	return previewOf(redactTurnText(s))
}

func previewOf(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > transcriptPreviewRunes {
		return string(r[:transcriptPreviewRunes]) + "…"
	}
	return s
}

func readOutline(src transcriptSource, cur TranscriptCursor, page *JobTranscriptPage) error {
	used := 0
	return readTurns(src, func(raw rawTurn) bool {
		if raw.Turn < cur.Turn {
			return true
		}
		// The outline redacts only the head it previews (redacting every full turn text
		// per listing would be the dominant cost); Chars is the stored text's size.
		e := JobTranscriptTurn{Turn: raw.Turn, Role: raw.Role, Chars: len(raw.Text), Preview: previewHead(raw.Text),
			Prompt: previewHead(promptBody(raw.Prompt)), Tools: raw.Tools, Tokens: raw.Tokens, Error: previewHead(raw.Error), Truncated: raw.Truncated}
		size := transcriptEntryOverhead + len(e.Preview) + len(e.Prompt) + len(e.Error)
		if used+size > TranscriptPageBytes && len(page.Turns) > 0 {
			page.Truncated, page.Next = true, &TranscriptCursor{Turn: raw.Turn}
			return false
		}
		used += size
		page.Turns = append(page.Turns, e)
		return true
	})
}

func readRange(src transcriptSource, from, to int, cur TranscriptCursor, page *JobTranscriptPage) error {
	if cur.Turn > from {
		from = cur.Turn
	}
	if from > to {
		return nil
	}
	used := 0
	return readTurns(src, func(raw rawTurn) bool {
		if raw.Turn < from {
			return true
		}
		if raw.Turn > to {
			return false
		}
		t := prepare(raw)
		start := 0
		if raw.Turn == cur.Turn {
			start = min(cur.Offset, len(t.Text))
			for start < len(t.Text) && !utf8.RuneStart(t.Text[start]) {
				start++
			}
		}
		prompt := ""
		if start == 0 {
			prompt = t.Prompt
		}
		remain := TranscriptPageBytes - used - transcriptEntryOverhead - len(prompt)
		if remain <= 0 && len(page.Turns) > 0 {
			page.Truncated, page.Next = true, &TranscriptCursor{Turn: t.Turn, Offset: start}
			return false
		}
		remain = max(remain, 1)
		end := len(t.Text)
		more := false
		if end-start > remain {
			end = start + cutBoundary(t.Text[start:], remain)
			more = true
			if end == start { // not even one rune fits: the page is full
				page.Truncated, page.Next = true, &TranscriptCursor{Turn: t.Turn, Offset: start}
				return false
			}
		}
		page.Turns = append(page.Turns, JobTranscriptTurn{Turn: t.Turn, Role: t.Role, Prompt: prompt, Text: t.Text[start:end],
			Offset: start, Chars: len(t.Text), Tools: t.Tools, Tokens: t.Tokens, Error: t.Error, Truncated: t.Truncated})
		used += transcriptEntryOverhead + len(prompt) + (end - start)
		if more {
			page.Truncated, page.Next = true, &TranscriptCursor{Turn: t.Turn, Offset: end}
			return false
		}
		if used >= TranscriptPageBytes && raw.Turn < to {
			page.Truncated, page.Next = true, &TranscriptCursor{Turn: t.Turn + 1}
			return false
		}
		return true
	})
}

func readSearch(src transcriptSource, term string, cur TranscriptCursor, page *JobTranscriptPage) error {
	used := 0
	return readTurns(src, func(raw rawTurn) bool {
		if raw.Turn < cur.Turn {
			return true
		}
		t := prepare(raw)
		count, snippets := 0, []string(nil)
		for _, field := range []string{t.Prompt, t.Text} {
			for off := 0; ; {
				i := strings.Index(field[off:], term)
				if i < 0 {
					break
				}
				at := off + i
				count++
				if len(snippets) < transcriptMaxSnippets {
					snippets = append(snippets, snippetAround(field, at, len(term)))
				}
				off = at + len(term)
			}
		}
		if count == 0 {
			return true
		}
		size := transcriptEntryOverhead
		for _, s := range snippets {
			size += len(s)
		}
		if used+size > TranscriptPageBytes && len(page.Turns) > 0 {
			page.Truncated, page.Next = true, &TranscriptCursor{Turn: t.Turn}
			return false
		}
		used += size
		page.Turns = append(page.Turns, JobTranscriptTurn{Turn: t.Turn, Role: t.Role, Chars: len(t.Text), Matches: snippets, MatchCount: count})
		return true
	})
}

// snippetAround returns the match with a little context on each side, cut on rune
// boundaries.
func snippetAround(s string, at, n int) string {
	lo := max(0, at-transcriptSnippetBytes/2)
	for lo < at && !utf8.RuneStart(s[lo]) {
		lo++
	}
	hi := min(len(s), at+n+transcriptSnippetBytes/2)
	hi = lo + cutBoundary(s[lo:], hi-lo)
	out := strings.ReplaceAll(s[lo:hi], "\n", " ")
	if lo > 0 {
		out = "…" + out
	}
	if hi < len(s) {
		out += "…"
	}
	return out
}
