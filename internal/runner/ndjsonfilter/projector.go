package ndjsonfilter

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/runner"
)

// Built-in projector kinds (Options.Projector). A projector turns ONE decoded
// agent event into the two capture streams (bd h-aii-525u): a compact event line
// for the log (stderr by default) and — for the line that carries the agent's
// final answer — the text that belongs on stdout.
//
// The kinds mirror the built-in capture defaults of the same name
// (agent.builtinNDJSON), resolved in internal/agent by agent key and then by the
// base name of the agent's command; an agent with no built-in projector gets
// ProjectorGeneric.
const (
	// ProjectorOMP projects an `omp --mode json` stream: per-token incremental
	// events are dropped, tool calls and turn summaries are compacted, and the
	// final answer is the last turn's assistant text.
	ProjectorOMP = "omp"
	// ProjectorClaude projects a `claude --output-format stream-json` stream: the
	// `result` line's text is the final answer, tool_use/tool_result blocks are
	// summarised, `stream_event` (per-token) is dropped.
	ProjectorClaude = "claude"
	// ProjectorGeneric is the fallback for an unknown command: every line the
	// whitelist lets through goes to the event stream unchanged (bounded), and
	// stdout stays empty unless Options.StdoutPath names where the answer lives.
	ProjectorGeneric = "generic"
)

// DefaultMaxEventBytes caps ONE line written to the event stream. An event is a
// log line, not a payload: 2KB is enough for a tool call summary plus its usage
// counters, and the agent's real output is on stdout (or in stdout.raw.log when
// ndjson_raw is on).
const DefaultMaxEventBytes = 2048

const (
	// maxSummaryRunes caps the human-readable summaries a built-in projector
	// embeds (tool arguments, tool results, tool_result bodies).
	maxSummaryRunes = 300
	// truncationMarker is appended to every value the capture shortened. It is
	// greppable on purpose: a truncated log line must say so.
	truncationMarker = "…(truncated)"
)

// event is one parsed agent line handed to a projector.
type event struct {
	typ string         // top-level `type`; "" when the line carries none
	obj map[string]any // the parsed JSON object
	raw []byte         // the line as the agent wrote it (whitespace trimmed)
}

// emission is a projector's decision about one event. Everything is optional: the
// zero value means "drop this line", which is what the per-token incremental
// events project to.
type emission struct {
	// Events is the compact event line for the event stream (nil = nothing).
	Events *eventLine
	// Verbatim is a line the projector cannot improve on: written to the event
	// stream as the agent emitted it, and only shortened when it exceeds the cap.
	Verbatim []byte
	// Text is a stdout fragment carried by this line (used by Options.StdoutPath).
	Text string
	// Session is the session id this line carries ("" = none).
	Session string
	// Usage is the token/cost accounting this line carries (SUP-01 E; nil = none).
	// The capture keeps the LAST one it sees, which is what an agent's running tally
	// means at the end of a run.
	Usage *runner.Usage
	// Kept marks a line whose information was retained although it produced no
	// output of its own (omp's message_end feeds the final answer).
	Kept bool
}

// keeps reports whether the emission retained any information; the complement is
// what the capture counts as a dropped line.
func (em emission) keeps() bool {
	return em.Kept || em.Events != nil || len(em.Verbatim) > 0 || em.Text != "" || em.Session != "" || em.Usage != nil
}

// projector is the per-agent event projection policy.
type projector interface {
	// project maps one parsed event to what the capture writes.
	project(ev event) emission
	// final returns the text to append to stdout once, when the run ends: the
	// agent's final answer. "" means the stream carried no answer.
	final() string
}

// newProjector resolves a projector kind; unknown kinds fall back to generic so a
// config typo can never swallow output.
func newProjector(kind string, allAssistant bool) projector {
	switch kind {
	case ProjectorOMP:
		return &ompProjector{all: allAssistant}
	case ProjectorClaude:
		return &claudeProjector{all: allAssistant}
	default:
		return genericProjector{}
	}
}

// eventLine is a compact event: `type` first, then the fields the projector kept,
// in the order it added them. An ordered line reads like the agent's own event in
// the log, which a Go map's key order does not.
type eventLine struct {
	typ    string
	fields []field
}

// field is one top-level key of an eventLine.
type field struct {
	key string
	val any
}

func newEventLine(typ string) *eventLine { return &eventLine{typ: typ} }

// add appends a field. Empty values are skipped (a `"provider":""` is noise in a
// log line) — but never a meaningful false/0, which stay.
func (e *eventLine) add(key string, val any) *eventLine {
	if val == nil {
		return e
	}
	if s, ok := val.(string); ok && s == "" {
		return e
	}
	e.fields = append(e.fields, field{key: key, val: val})
	return e
}

// MarshalJSON lets an eventLine nest inside another one (a tool summary inside an
// assistant event) while keeping its field order.
func (e *eventLine) MarshalJSON() ([]byte, error) { return e.marshal(), nil }

// marshal renders the event as `{"type":"turn_end","model":"omp-1",...}`.
func (e *eventLine) marshal() []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	buf.WriteString(`"type":`)
	writeJSON(&buf, e.typ)
	for _, f := range e.fields {
		buf.WriteByte(',')
		writeJSON(&buf, f.key)
		buf.WriteByte(':')
		writeJSON(&buf, f.val)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// trimmed renders the event within max bytes. Note this MUTATES the line: string
// values (nested ones included) are shortened until the JSON fits, and every
// shortened value carries truncationMarker. It reports whether it had to shorten
// anything. An event of only keys and numbers cannot be shortened — such a line
// is returned over the cap rather than mangled.
func (e *eventLine) trimmed(max int) ([]byte, bool) {
	b := e.marshal()
	if len(b) <= max {
		return b, false
	}
	truncated := false
	for i := 0; i < 32; i++ {
		changed := false
		for j := range e.fields {
			if v, ok := trimValue(e.fields[j].val); ok {
				e.fields[j].val = v
				changed = true
			}
		}
		if !changed {
			break
		}
		truncated = true
		if b = e.marshal(); len(b) <= max {
			return b, true
		}
	}
	return e.marshal(), truncated
}

// trimValue halves every string inside v (maps, slices and nested eventLines
// included) and reports whether anything changed. "Nothing changed" is the
// caller's stop signal: only keys and numbers are left, so no further halving can
// shrink the JSON.
func trimValue(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		if s, ok := shorten(t); ok {
			return s, true
		}
		return t, false
	case map[string]any:
		changed := false
		for k, vv := range t {
			if nv, ok := trimValue(vv); ok {
				t[k] = nv
				changed = true
			}
		}
		return t, changed
	case []any:
		changed := false
		for i, vv := range t {
			if nv, ok := trimValue(vv); ok {
				t[i] = nv
				changed = true
			}
		}
		return t, changed
	case *eventLine:
		changed := false
		for i := range t.fields {
			if nv, ok := trimValue(t.fields[i].val); ok {
				t.fields[i].val = nv
				changed = true
			}
		}
		return t, changed
	}
	return v, false
}

// shorten halves s (rune-safe). An already-marked value is un-marked first, so
// repeated halving never stacks markers.
func shorten(s string) (string, bool) {
	body := strings.TrimSuffix(s, truncationMarker)
	n := utf8.RuneCountInString(body) / 2
	if n < 1 {
		return s, false
	}
	out := truncateRunes(body, n)
	if out == s {
		return s, false
	}
	return out, true
}

// truncateRunes cuts s to n runes and marks it; a string already within n runes
// is returned unchanged (never marked).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + truncationMarker
}

// truncateText cuts raw text (a stray non-JSON line, an oversize line the capture
// refused to parse) to at most max bytes INCLUDING the truncation marker, on a
// rune boundary.
func truncateText(b []byte, max int) []byte {
	b = bytes.TrimRight(b, "\r\n")
	if len(b) <= max {
		return b
	}
	if max <= len(truncationMarker) {
		return []byte(truncationMarker[:max])
	}
	head := b[:max-len(truncationMarker)]
	for len(head) > 0 && !utf8.Valid(head) {
		head = head[:len(head)-1]
	}
	out := make([]byte, 0, len(head)+len(truncationMarker))
	out = append(out, head...)
	return append(out, truncationMarker...)
}

// writeJSON appends v as JSON, falling back to `null` for the (unreachable for
// parsed input) values encoding/json rejects — an event line must never be
// dropped because one field could not be encoded.
func writeJSON(buf *bytes.Buffer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		buf.WriteString("null")
		return
	}
	buf.Write(b)
}

// lineFromObject builds an eventLine from a whole parsed event, keeping every
// top-level key (sorted, so the output is deterministic) — the shape used for a
// pass-through line that is too long to write verbatim.
func lineFromObject(typ string, obj map[string]any) *eventLine {
	line := newEventLine(typ)
	keys := make([]string, 0, len(obj))
	for k := range obj {
		if k == "type" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		line.add(k, obj[k])
	}
	return line
}

// fieldLine builds an eventLine carrying exactly the configured `ndjson_fields`
// entries for a type (dotted paths allowed; the emitted key is the path's last
// segment). It overrides the built-in event shape for that type.
func fieldLine(typ string, obj map[string]any, fields []string) *eventLine {
	line := newEventLine(typ)
	for _, entry := range fields {
		entry = strings.TrimSpace(entry)
		if entry == "" || entry == "type" {
			continue
		}
		path := strings.Split(entry, ".")
		v, ok := lookup(obj, path)
		if !ok {
			continue
		}
		line.add(path[len(path)-1], v)
	}
	return line
}

// ompProjector projects an `omp --mode json` stream. The per-token
// message_update / tool_execution_update events dominate the volume and are
// dropped; what the caller actually wants from a tool-heavy run — the final
// answer — is the LAST completed assistant message, written to stdout once when
// the run ends (bd h-aii-525u).
type ompProjector struct {
	all       bool     // Options.AllAssistantText: stdout = every assistant text, not just the last
	texts     []string // every non-empty completed assistant text, in order (all mode)
	finalText string   // text of the most recent completed assistant message
}

func (p *ompProjector) project(ev event) emission {
	switch ev.typ {
	case "session":
		sid := stringField(ev.obj, "id")
		return emission{
			Events:  newEventLine(ev.typ).add("id", sid).add("model", ev.obj["model"]).add("cwd", ev.obj["cwd"]),
			Session: sid,
		}
	case "message_end":
		// A completed assistant message: remember its text (the LAST one is the
		// run's answer) and its usage counters (the LAST message carries the run's
		// final tally), write nothing — stdout gets the answer once, at the end.
		if pathString(ev.obj, "message", "role") == "assistant" {
			em := emission{Usage: usageAt(ev.obj, runner.UsageSourceNDJSONOMP, "message", "usage")}
			if t := assistantText(ev.obj); t != "" {
				p.finalText = t
				if p.all {
					p.texts = append(p.texts, t)
				}
				em.Kept = true
			}
			return em
		}
		return emission{}
	case "turn_end":
		return emission{Events: newEventLine(ev.typ).
			add("model", ev.obj["model"]).
			add("provider", ev.obj["provider"]).
			add("usage", ev.obj["usage"]).
			add("stop_reason", firstString(ev.obj, "stop_reason", "stopReason"))}
	case "tool_execution_start":
		return emission{Events: newEventLine(ev.typ).
			add("toolName", firstString(ev.obj, "toolName", "tool")).
			add("intent", ev.obj["intent"]).
			add("args", summarizeValue(ev.obj["args"], maxSummaryRunes))}
	case "tool_execution_end":
		return emission{Events: newEventLine(ev.typ).
			add("toolName", firstString(ev.obj, "toolName", "tool")).
			add("ok", toolOK(ev.obj)).
			add("result", summarizeValue(ev.obj["result"], maxSummaryRunes))}
	case "message_update", "tool_execution_update", "message_start", "turn_start":
		// Per-token / lifecycle noise: the whole point of the capture filter.
		return emission{}
	}
	// Anything else omp emits (agent_end, advisor_cost_changed, a future event)
	// is kept as it wrote it, bounded by the event cap.
	return emission{Verbatim: ev.raw}
}

func (p *ompProjector) final() string {
	if p.all {
		return joinTexts(p.texts)
	}
	return p.finalText
}

// joinTexts renders the all-assistant-text stdout: each message once, trimmed of
// trailing newlines, blank-line separated, ending in a newline like a plain answer.
func joinTexts(texts []string) string {
	var b strings.Builder
	for _, t := range texts {
		t = strings.TrimRight(t, "\r\n")
		if t == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(t)
	}
	return b.String()
}

// claudeProjector projects a `claude --output-format stream-json` stream. The
// final answer is the `result` line's `result` text; assistant/user lines are
// compacted to what a reader needs (which tool ran with what, how much text) and
// `stream_event` (per-token) is dropped.
type claudeProjector struct {
	all       bool     // Options.AllAssistantText: stdout = every assistant text (+ result when it adds something)
	texts     []string // every non-empty assistant text, in order (all mode)
	finalText string   // result.result
	lastText  string   // last assistant message text: fallback when the run dies first
}

func (p *claudeProjector) project(ev event) emission {
	switch ev.typ {
	case "system":
		sid := stringField(ev.obj, "session_id")
		line := newEventLine(ev.typ).
			add("subtype", ev.obj["subtype"]).
			add("session_id", sid).
			add("model", ev.obj["model"]).
			add("cwd", ev.obj["cwd"])
		if tools, ok := ev.obj["tools"].([]any); ok {
			line.add("tool_count", len(tools))
		}
		return emission{Events: line, Session: sid}
	case "assistant":
		blocks := contentBlocks(ev.obj)
		text := textFromBlocks(blocks)
		if text != "" {
			p.lastText = text
			if p.all {
				p.texts = append(p.texts, text)
			}
		}
		return emission{Events: newEventLine(ev.typ).
			add("tools", toolUseSummaries(blocks)).
			add("text_len", utf8.RuneCountInString(text))}
	case "user":
		return emission{Events: newEventLine(ev.typ).
			add("tool_result", summarizeValue(toolResults(ev.obj), maxSummaryRunes))}
	case "result":
		if s := stringField(ev.obj, "result"); s != "" {
			p.finalText = s
		}
		return emission{
			Usage: claudeUsage(ev.obj),
			Events: newEventLine(ev.typ).
				add("subtype", ev.obj["subtype"]).
				add("is_error", ev.obj["is_error"]).
				add("usage", ev.obj["usage"]).
				add("total_cost_usd", ev.obj["total_cost_usd"]).
				add("duration_ms", ev.obj["duration_ms"]).
				add("num_turns", ev.obj["num_turns"])}
	case "stream_event":
		// Per-token stream deltas: the whole point of the capture filter.
		return emission{}
	}
	return emission{Verbatim: ev.raw}
}

// final prefers the `result` text and falls back to the last assistant message:
// a run killed by a timeout/job ceiling never emits `result`, and half an answer
// on stdout beats an empty one.
func (p *claudeProjector) final() string {
	if p.all {
		// result.result repeats the last assistant text; append it only when the
		// run's summary says something the messages did not.
		texts := p.texts
		if p.finalText != "" && (len(texts) == 0 || strings.TrimSpace(texts[len(texts)-1]) != strings.TrimSpace(p.finalText)) {
			texts = append(append([]string(nil), texts...), p.finalText)
		}
		return joinTexts(texts)
	}
	if p.finalText != "" {
		return p.finalText
	}
	return p.lastText
}

// genericProjector is the fallback for an unknown command: the whitelist decides
// what is read, the line itself is written to the event stream unchanged (bounded
// by the cap), and the session row shapes it knows are extracted. Nothing goes to
// stdout unless the config names a path (Options.StdoutPath).
type genericProjector struct{}

func (genericProjector) project(ev event) emission {
	em := emission{Verbatim: ev.raw}
	switch {
	case stringField(ev.obj, "session_id") != "":
		em.Session = stringField(ev.obj, "session_id")
	case ev.typ == "session":
		em.Session = stringField(ev.obj, "id")
	}
	return em
}

func (genericProjector) final() string { return "" }

// stringField reads a top-level string field ("" when absent or of another type).
func stringField(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}

// firstString returns the first non-empty top-level string among keys.
func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := stringField(obj, k); s != "" {
			return s
		}
	}
	return ""
}

// pathString reads a nested string field ("" when absent or of another type).
func pathString(obj map[string]any, path ...string) string {
	v, ok := lookup(obj, path)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// assistantText assembles the assistant text of an omp message_end event: the
// `message.content[]` text parts, or a bare string content.
func assistantText(obj map[string]any) string {
	msg, ok := obj["message"].(map[string]any)
	if !ok {
		return ""
	}
	return textFromContent(msg["content"])
}

// textFromContent joins the text parts of a message content value, which may be a
// plain string, an array of parts (`{type:"text",text}`), or something else.
func textFromContent(content any) string {
	switch t := content.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, part := range t {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := m["text"].(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// contentBlocks returns the `message.content[]` blocks of a claude event.
func contentBlocks(obj map[string]any) []any {
	msg, ok := obj["message"].(map[string]any)
	if !ok {
		return nil
	}
	blocks, _ := msg["content"].([]any)
	return blocks
}

// textFromBlocks concatenates the `text` blocks of a claude message.
func textFromBlocks(blocks []any) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := m["type"].(string); typ != "text" {
			continue
		}
		if s, ok := m["text"].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// toolUseSummaries compacts the tool_use blocks of a claude assistant message to
// `{name, input}` with the input summarised.
func toolUseSummaries(blocks []any) []any {
	var out []any
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := m["type"].(string); typ != "tool_use" {
			continue
		}
		out = append(out, newEventLine("tool_use").
			add("name", stringField(m, "name")).
			add("input", summarizeValue(m["input"], maxSummaryRunes)))
	}
	return out
}

// toolResults compacts the tool_result blocks of a claude user message: their
// content collapsed to one summarised string.
func toolResults(obj map[string]any) string {
	var parts []string
	for _, b := range contentBlocks(obj) {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := m["type"].(string); typ != "tool_result" {
			continue
		}
		if s := textFromContent(m["content"]); s != "" {
			parts = append(parts, s)
			continue
		}
		if s, ok := m["content"].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// toolOK reads the error flag of an omp tool_execution_end event: `result.isError`
// is the documented shape, a top-level `isError` is accepted too. Absent means ok.
func toolOK(obj map[string]any) bool {
	if v, ok := lookup(obj, []string{"result", "isError"}); ok {
		if b, isBool := v.(bool); isBool {
			return !b
		}
	}
	if b, isBool := obj["isError"].(bool); isBool {
		return !b
	}
	return true
}

// summarizeValue renders v as a bounded, single-line summary: a string is cut to
// n runes, anything else is compact JSON cut to n runes. nil stays nil (the field
// is then omitted).
func summarizeValue(v any, n int) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return oneLine(truncateRunes(t, n))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return oneLine(truncateRunes(string(b), n))
}

// oneLine collapses a summary to one line: an event is one log line, and a
// multi-line tool result would otherwise break it apart.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	return strings.Join(lines, " ⏎ ")
}
