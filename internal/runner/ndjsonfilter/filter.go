// Package ndjsonfilter compacts a structured (NDJSON / JSON-Lines) agent output
// stream at CAPTURE time (bd h-aii-rpky).
//
// An `omp --mode json` or `claude --output-format stream-json` run emits one JSON
// event per line, and the incremental per-token events (message_update /
// tool_execution_update / stream_event) dominate the volume: written verbatim into
// stdout.log they inflate the log 10–30x and make the live web log unreadable. A
// Filter wraps the log writer and keeps only the events worth reading.
//
// Guarantees:
//   - Output stays valid JSONL: only WHOLE lines are ever written through, so a
//     filtered stream is still a JSON-Lines file. A line that cannot be parsed
//     (or is not a JSON object) is passed through VERBATIM — filtering must never
//     lose output it does not understand.
//   - The `session` row is always kept, whatever the whitelist says: gofer's
//     session capture / resume reads it out of stdout.log (omp especially, whose
//     session row only exists in json mode).
//   - An empty whitelist means "no filtering at all": every line is written
//     through unchanged. A text agent is therefore unaffected by construction.
//   - A line longer than MaxLineBytes (1MB by default) is neither parsed nor
//     buffered: it streams through verbatim.
//
// It is an io.Writer, so it drops into any capture path (the job service wraps
// the stdout.log writer with it), and it counts what it did: Kept+Dropped equals
// the number of lines seen, which is what the job result reports as
// ndjson_kept / ndjson_dropped.
package ndjsonfilter

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// DefaultMaxLineBytes is the parse cap: a longer line is not worth parsing (and
// must not be buffered whole just to be discarded).
const DefaultMaxLineBytes = 1 << 20

// alwaysKeepType is the event type the filter never drops: the session row that
// session capture / resume depends on.
const alwaysKeepType = "session"

// Options configures a Filter.
type Options struct {
	// Keep is the event-type whitelist. A bare entry ("message_end") matches the
	// JSON top-level `type`; a dotted entry ("message.role") is a nested path and
	// matches when that path holds a non-empty string. Empty = keep everything.
	Keep []string
	// Raw, when non-nil, receives every line VERBATIM (dropped ones included)
	// before filtering — the stdout.raw.log sidecar for troubleshooting. Write
	// errors are ignored: a debugging artifact must never break a job.
	Raw io.Writer
	// MaxLineBytes overrides DefaultMaxLineBytes; <= 0 uses the default.
	MaxLineBytes int
}

// matcher is one compiled whitelist entry.
type matcher struct {
	path []string // JSON path to read; ["type"] for a bare entry
	want string   // expected value; "" for a dotted entry (presence of a string)
}

// Filter is an io.Writer that keeps only whitelisted JSON events and passes
// non-JSON lines through untouched. It is NOT safe for concurrent writers — it is
// wired to one child process' stdout.
type Filter struct {
	w        io.Writer
	raw      io.Writer
	matchers []matcher
	maxLine  int
	keepAll  bool

	pending  []byte // bytes of the current, not-yet-terminated line
	longLine bool   // inside an over-long line: stream it through without parsing

	kept    int
	dropped int
}

// New builds a Filter over w.
func New(w io.Writer, opt Options) *Filter {
	f := &Filter{w: w, raw: opt.Raw, maxLine: opt.MaxLineBytes}
	if f.maxLine <= 0 {
		f.maxLine = DefaultMaxLineBytes
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

// Write implements io.Writer. Bytes are always accepted in full (the filter never
// rejects output); only a failing underlying writer surfaces as an error.
func (f *Filter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		if f.longLine {
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				if err := f.emit(p); err != nil {
					return 0, err
				}
				break
			}
			if err := f.emit(p[:i+1]); err != nil {
				return 0, err
			}
			p = p[i+1:]
			f.longLine = false
			f.kept++
			continue
		}

		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			f.pending = append(f.pending, p...)
			if len(f.pending) > f.maxLine {
				// Past the parse cap: flush what we hold and stream the rest of the
				// line through verbatim (it is counted once the line completes).
				if err := f.emit(f.pending); err != nil {
					return 0, err
				}
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
	// Over the parse cap: never parse it, never drop it — pass it through verbatim.
	if len(line) > f.maxLine+1 {
		f.kept++
		return f.emit(line)
	}
	if f.keepLine(line) {
		f.kept++
		return f.emit(line)
	}
	f.dropped++
	return nil
}

// emit writes b straight to the underlying writer.
func (f *Filter) emit(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	_, err := f.w.Write(b)
	return err
}

// keepLine reports whether line survives the filter.
func (f *Filter) keepLine(line []byte) bool {
	if f.keepAll {
		return true
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return true // not a JSON object: pass it through unparsed
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return true // unparseable: never drop what we cannot read
	}
	if typ, ok := obj["type"].(string); ok && typ == alwaysKeepType {
		return true
	}
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

// Close flushes a final line that arrived without its newline (the child died
// mid-write) so the tail of the stream is never lost. It does not close the
// underlying writer(s): the caller owns them.
func (f *Filter) Close() error {
	if f.longLine {
		f.longLine = false
		f.kept++ // an over-long final line was streamed through verbatim
	}
	if len(f.pending) == 0 {
		return nil
	}
	line := f.pending
	f.pending = nil
	if f.raw != nil {
		_, _ = f.raw.Write(line)
	}
	f.kept++
	return f.emit(line)
}

// Counts returns the number of lines kept in and dropped from the captured
// stream. Kept counts every line that survived (whitelisted JSON events, and
// non-JSON/oversize lines passed through verbatim), so Kept+Dropped is the total
// number of lines the agent emitted.
func (f *Filter) Counts() (kept, dropped int) { return f.kept, f.dropped }
