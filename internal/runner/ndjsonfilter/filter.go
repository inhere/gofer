// Package ndjsonfilter turns a structured (NDJSON / JSON-Lines) agent output
// stream into the two streams gofer captures (bd h-aii-rpky, bd h-aii-525u).
//
// An `omp --mode json` or `claude --output-format stream-json` run emits one JSON
// event per line. Two things are wrong with writing that stream straight to
// stdout.log: the per-token incremental events (message_update /
// tool_execution_update / stream_event) inflate the log 10-30x and make the live
// web log unreadable, and the caller's actual deliverable — the agent's final
// answer — is buried in the middle of it, after the events. So the capture
// PROJECTS the stream instead of merely filtering it:
//
//   - stdout gets the agent's FINAL text, written once when the run ends (the
//     last assistant turn for omp, the `result` text for claude).
//   - the event stream (stderr by default, like codex) gets one compact line per
//     information-bearing event, each capped at MaxEventBytes with its long
//     fields marked `…(truncated)`.
//
// Guarantees:
//   - Output stays valid JSONL for every line the capture could PARSE: only whole
//     lines are ever written, and only the event stream gets JSON. A line it
//     cannot parse (plain text, broken JSON, an over-long line it refuses to
//     buffer) is never dropped — it lands on the event stream, bounded. A blank
//     line is the one exception: it carries nothing, so it is dropped.
//   - The `session` row is always read, whatever the whitelist says: the capture
//     reports its id for session capture / resume (omp especially, whose session
//     row only exists in json mode), and the row is kept in the event stream.
//   - An empty whitelist means "no filtering at all": every line reaches the
//     projector. A text agent is therefore unaffected by construction — it is not
//     wrapped at all.
//   - A line longer than MaxLineBytes (1MB by default) is neither parsed nor
//     buffered whole; its head is kept for the event stream (which caps at 2KB
//     anyway). A final answer that huge is out of scope — keep ndjson_raw on if
//     your agent can produce one.
//
// It is an io.Writer, so it drops into the capture path (the job service wraps
// the stdout.log writer with it and hands over stderr.log as the event stream),
// and it counts what it did: Kept+Dropped equals the number of lines seen, which
// is what the job result reports as ndjson_kept / ndjson_dropped, plus
// ndjson_truncated for the lines the event cap shortened.
package ndjsonfilter

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/inhere/gofer/internal/runner"
)

// DefaultMaxLineBytes is the parse cap: a longer line is not worth parsing (and
// must not be buffered whole just to be discarded).
const DefaultMaxLineBytes = 1 << 20

// alwaysKeepType is the event type the capture never drops: the session row that
// session capture / resume depends on.
const alwaysKeepType = "session"

// Options configures a Filter.
type Options struct {
	// Keep is the event-type whitelist. A bare entry ("message_end") matches the
	// JSON top-level `type`; a dotted entry ("message.role") is a nested path and
	// matches when that path holds a non-empty string. Empty = keep everything.
	// It gates what reaches the projector; the session row always does.
	Keep []string
	// Raw, when non-nil, receives every line VERBATIM (dropped ones included,
	// before any projection) — the stdout.raw.log sidecar for troubleshooting.
	// Write errors are ignored: a debugging artifact must never break a job.
	Raw io.Writer
	// MaxLineBytes overrides DefaultMaxLineBytes; <= 0 uses the default.
	MaxLineBytes int
	// Projector selects the built-in projection rules (ProjectorOMP /
	// ProjectorClaude / ProjectorGeneric). "" or an unknown value falls back to
	// ProjectorGeneric, so a config typo can never lose output.
	Projector string
	// EventsToStdout routes the event stream to the STDOUT writer instead of the
	// event writer (the pre-525u behaviour, for anyone who wants the events where
	// they used to be).
	EventsToStdout bool
	// StdoutEvents puts the compact events on stdout and disables final-text
	// extraction entirely (the pre-525u behaviour of the capture).
	StdoutEvents bool
	// AllAssistantText makes stdout carry EVERY non-empty assistant message the
	// stream completed, in order and blank-line separated (the agent's narration
	// plus its final answer), instead of only the last one. It exists because the
	// "last assistant message" is not always the answer: a harness that injects a
	// trailing notification (a todo reminder, a background-task notice) makes the
	// agent reply once more with a one-liner, and the real report before it was
	// lost (bd h-aii-lvo9). The intermediate texts are short (tens of chars each,
	// a few KB per run), so this stays far from the raw stream it replaces.
	AllAssistantText bool
	// StdoutPath is a dotted JSON path (e.g. "result.result") whose value is the
	// agent's final answer. Set it for an agent whose final text the built-in
	// projectors do not know how to find; it overrides their final-text rules and
	// writes the value as soon as the line carrying it arrives.
	StdoutPath string
	// Fields overrides the emitted event content per event type (the
	// `ndjson_fields` config): the line carries exactly those dotted paths, in
	// that order. It replaces the built-in shape for that type, and works for a
	// type the projector would otherwise drop.
	Fields map[string][]string
	// MaxEventBytes overrides DefaultMaxEventBytes; <= 0 uses the default.
	MaxEventBytes int
}

// matcher is one compiled whitelist entry.
type matcher struct {
	path []string // JSON path to read; ["type"] for a bare entry
	want string   // expected value; "" for a dotted entry (presence of a string)
}

// Filter is an io.Writer that projects an NDJSON agent stream onto two sinks: the
// agent's final text (stdout.log) and the compact event stream (stderr.log). It
// is NOT safe for concurrent writers — it is wired to one child process' stdout.
type Filter struct {
	text   io.Writer // final-text sink; nil when the events take stdout instead
	events io.Writer // event sink
	raw    io.Writer
	proj   projector

	matchers []matcher
	fields   map[string][]string
	stdoutAt []string // dotted StdoutPath, nil = use the projector's final text
	maxLine  int
	maxEvent int
	keepAll  bool

	pending  []byte // bytes of the current, not-yet-terminated line
	longLine bool   // inside an over-long line: only its head is kept
	head     []byte // the head kept for an over-long line

	sessionID string
	finalSent bool
	// usage is the LAST usage the projected stream carried (SUP-01 E): an agent
	// reports a running tally, so the last one is the run's final accounting.
	usage *runner.Usage

	kept      int
	dropped   int
	truncated int
}

// New builds a Filter. stdout receives the agent's final text and events the
// compact event lines; Options.EventsToStdout / Options.StdoutEvents may route
// the events back onto stdout.
func New(stdout, events io.Writer, opt Options) *Filter {
	f := &Filter{raw: opt.Raw, proj: newProjector(opt.Projector, opt.AllAssistantText), fields: opt.Fields, maxLine: opt.MaxLineBytes, maxEvent: opt.MaxEventBytes}
	if f.maxLine <= 0 {
		f.maxLine = DefaultMaxLineBytes
	}
	if f.maxEvent <= 0 {
		f.maxEvent = DefaultMaxEventBytes
	}
	switch {
	case opt.StdoutEvents:
		// Legacy: stdout carries the events, no final-text extraction.
		f.events, f.text = stdout, nil
	case opt.EventsToStdout:
		f.events, f.text = stdout, stdout
	default:
		f.events, f.text = events, stdout
	}
	if p := strings.TrimSpace(opt.StdoutPath); p != "" {
		f.stdoutAt = strings.Split(p, ".")
	}
	for _, entry := range opt.Keep {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if i := strings.IndexByte(entry, '.'); i > 0 && i < len(entry)-1 {
			f.matchers = append(f.matchers, matcher{path: strings.Split(entry, ".")})
			continue
		}
		f.matchers = append(f.matchers, matcher{path: []string{"type"}, want: entry})
	}
	f.keepAll = len(f.matchers) == 0
	return f
}

// Write implements io.Writer. Bytes are always accepted in full (the capture
// never rejects output); only a failing underlying writer surfaces as an error.
func (f *Filter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		if f.longLine {
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				// Still inside the over-long line: its head is already held, the
				// rest is skipped rather than buffered.
				break
			}
			p = p[i+1:]
			f.longLine = false
			if err := f.flushLong(); err != nil {
				return 0, err
			}
			continue
		}

		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			f.pending = append(f.pending, p...)
			if len(f.pending) > f.maxLine {
				// Past the parse cap: an oversize line is never parsed. Keep its
				// head for the event stream (which caps at 2KB regardless) and skip
				// the remainder.
				f.head = truncateText(f.pending, f.maxEvent)
				f.pending = f.pending[:0]
				f.longLine = true
			}
			break
		}

		f.pending = append(f.pending, p[:i+1]...)
		line := f.pending
		p = p[i+1:]
		err := f.writeLine(line)
		f.pending = f.pending[:0] // the line has been written; the buffer is reusable
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

// writeLine handles one complete line (newline included).
func (f *Filter) writeLine(line []byte) error {
	if f.raw != nil {
		_, _ = f.raw.Write(line)
	}
	// Over the parse cap: never parse it, never drop it — its head goes to the
	// event stream.
	if len(line) > f.maxLine+1 {
		f.head = truncateText(line, f.maxEvent)
		return f.flushLong()
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		// A blank line carries nothing for either stream (it is counted as
		// dropped, so the audit still adds up to the lines the agent emitted).
		f.dropped++
		return nil
	}

	var obj map[string]any
	if trimmed[0] == '{' {
		_ = json.Unmarshal(trimmed, &obj) // a failure leaves obj nil: handled below
	}
	if obj == nil {
		// Not a JSON object (plain text, a JSON array, broken JSON): never drop
		// what we cannot read — it goes to the event stream, bounded, with its own
		// spacing intact.
		f.kept++
		return f.emitEventText(bytes.TrimRight(line, "\r\n"))
	}

	typ, _ := obj["type"].(string)
	if !f.keepAll && typ != alwaysKeepType && !f.matches(obj) {
		f.dropped++
		return nil
	}

	ev := event{typ: typ, obj: obj, raw: trimmed}
	em := f.proj.project(ev)
	if fields, ok := f.fields[typ]; ok {
		// A configured field list replaces the built-in event shape (and can
		// surface a type the projector drops).
		em.Events, em.Verbatim = fieldLine(typ, obj, fields), nil
	}
	if f.stdoutAt != nil {
		if v, ok := lookup(obj, f.stdoutAt); ok {
			em.Text = valueText(v)
		}
	}
	if em.keeps() {
		f.kept++
	} else {
		f.dropped++
	}
	if em.Session != "" && f.sessionID == "" {
		f.sessionID = em.Session
	}
	if em.Usage != nil {
		f.usage = em.Usage
	}
	if em.Text != "" {
		if err := f.emitText(em.Text); err != nil {
			return err
		}
	}
	return f.emitEvent(ev, em)
}

// emitEvent writes one projected event to the event stream.
func (f *Filter) emitEvent(ev event, em emission) error {
	switch {
	case em.Events != nil:
		b, shortened := em.Events.trimmed(f.maxEvent)
		if shortened {
			f.truncated++
		}
		return f.emitEvents(b)
	case len(em.Verbatim) > 0:
		if len(em.Verbatim) <= f.maxEvent {
			return f.emitEvents(em.Verbatim)
		}
		// Too long to pass through: shorten the parsed fields instead of cutting
		// the JSON in half, so the stream stays parseable.
		b, _ := lineFromObject(ev.typ, ev.obj).trimmed(f.maxEvent)
		f.truncated++
		return f.emitEvents(b)
	}
	return nil
}

// emitEvents writes one finished event line.
func (f *Filter) emitEvents(b []byte) error {
	if f.events == nil || len(b) == 0 {
		return nil
	}
	if _, err := f.events.Write(b); err != nil {
		return err
	}
	_, err := f.events.Write([]byte("\n"))
	return err
}

// emitEventText writes a text line (stray output the capture could not parse)
// to the event stream, cut to the event cap.
func (f *Filter) emitEventText(b []byte) error {
	if len(b) > f.maxEvent {
		b = truncateText(b, f.maxEvent)
		f.truncated++
	}
	return f.emitEvents(b)
}

// emitText appends a stdout fragment, newline-terminated so consecutive fragments
// and an answer the agent did not newline-terminate both stay readable line by
// line.
func (f *Filter) emitText(s string) error {
	if f.text == nil || s == "" {
		return nil
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	_, err := io.WriteString(f.text, s)
	return err
}

// flushLong finishes an over-long line: its head (already bounded) goes to the
// event stream, since output the capture could not fully read must still be
// visible.
func (f *Filter) flushLong() error {
	head := f.head
	f.head = nil
	f.kept++
	f.truncated++
	if len(head) == 0 {
		return nil
	}
	return f.emitEvents(head)
}

// matches reports whether a parsed event satisfies the whitelist.
func (f *Filter) matches(obj map[string]any) bool {
	for _, m := range f.matchers {
		v, ok := lookup(obj, m.path)
		if !ok {
			continue
		}
		s, isStr := v.(string)
		if m.want == "" {
			if isStr && s != "" {
				return true
			}
			continue
		}
		if isStr && s == m.want {
			return true
		}
	}
	return false
}

// lookup walks a JSON path through nested objects.
func lookup(obj map[string]any, path []string) (any, bool) {
	var cur any = obj
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// valueText renders a stdout path value: a string as-is, anything else as its
// compact JSON.
func valueText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// Close flushes what is left, then writes the agent's final answer to stdout if
// the run produced one. It does not close the underlying writers: the caller owns
// them. Calling it twice is harmless (the answer is emitted once).
func (f *Filter) Close() error {
	if f.longLine {
		f.longLine = false
		if err := f.flushLong(); err != nil {
			return err
		}
	}
	if len(f.pending) > 0 {
		// A final line that arrived without its newline (the child died mid-write)
		// is projected like any other, so its tail is never lost.
		line := f.pending
		f.pending = nil
		if err := f.writeLine(line); err != nil {
			return err
		}
	}
	if f.stdoutAt != nil || f.finalSent {
		return nil
	}
	f.finalSent = true
	return f.emitText(f.proj.final())
}

// SessionID returns the session id the projected stream carried ("" when it
// carried none). It is the capture's own answer, and takes precedence over the
// regex capture that scans the log files.
func (f *Filter) SessionID() string { return f.sessionID }

// Counts returns the number of lines kept in and dropped from the captured
// stream. Kept counts every line whose information was retained (an event, a
// stdout fragment, a session id, or a final-answer source); Kept+Dropped is the
// total number of lines the agent emitted.
func (f *Filter) Counts() (kept, dropped int) { return f.kept, f.dropped }

// Truncated returns how many event-stream lines the MaxEventBytes cap shortened.
func (f *Filter) Truncated() int { return f.truncated }

// Usage returns the token/cost accounting the projected stream carried (SUP-01 E):
// the LAST usage an agent reported, or nil when the stream carried none. Like
// SessionID it is the capture's own answer — the projector knows which row of the
// agent's stream holds the run's final tally, a log scan does not.
func (f *Filter) Usage() *runner.Usage { return f.usage }
