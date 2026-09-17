package ndjsonfilter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ompKeep mirrors the built-in omp whitelist (internal/agent/registry.go): the
// events worth keeping from an `omp --mode json` stream. The incremental-token
// events (message_update / tool_execution_update / message_start / turn_start)
// are deliberately absent.
var ompKeep = []string{
	"session",
	"tool_execution_start",
	"tool_execution_end",
	"message_end",
	"turn_end",
	"agent_end",
	"advisor_cost_changed",
}

// ompSample builds a 60-line sample of an `omp --mode json` event stream: the
// head/tail of a two-turn run whose per-token message_update events dominate the
// volume (jsonlSampleSize lines). It is the fixture the "60 lines -> <=12 lines"
// claim of the capture filter is measured against.
func ompSample() []string {
	const perTurnUpdates = 19 // 19 + 9 other events per turn = 28; 1 + 2*28 = 57
	lines := []string{
		`{"type":"session","id":"0f9c1e2a-1111-4a2b-8c3d-9e8f7a6b5c4d","model":"omp-1","cwd":"D:/work/x"}`,
	}
	for turn := 1; turn <= 2; turn++ {
		lines = append(lines,
			fmt.Sprintf(`{"type":"turn_start","turn":%d}`, turn),
			fmt.Sprintf(`{"type":"message_start","turn":%d,"message":{"role":"assistant","content":[]}}`, turn),
		)
		for i := 0; i < perTurnUpdates; i++ {
			lines = append(lines, fmt.Sprintf(
				`{"type":"message_update","turn":%d,"seq":%d,"message":{"role":"assistant","content":[{"type":"text","text":"tok%d"}]}}`, turn, i, i))
		}
		lines = append(lines, fmt.Sprintf(
			`{"type":"tool_execution_start","turn":%d,"toolCallId":"call_%d","toolName":"bash","intent":"list the project root","args":{"command":"ls -la"}}`, turn, turn))
		for i := 0; i < 3; i++ {
			lines = append(lines, fmt.Sprintf(
				`{"type":"tool_execution_update","turn":%d,"toolCallId":"call_%d","partialOutput":"chunk%d"}`, turn, turn, i))
		}
		lines = append(lines,
			fmt.Sprintf(`{"type":"tool_execution_end","turn":%d,"toolCallId":"call_%d","toolName":"bash","result":{"stdout":"total 4","isError":false}}`, turn, turn),
			fmt.Sprintf(`{"type":"message_end","turn":%d,"message":{"role":"assistant","content":[{"type":"text","text":"turn %d done"}]},"usage":{"input_tokens":100,"output_tokens":20}}`, turn, turn),
			fmt.Sprintf(`{"type":"turn_end","turn":%d,"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120},"model":"omp-1"}`, turn),
		)
	}
	// Pad up to 58 lines with the very noise the filter exists to drop, then close
	// the stream with the run's summary events -> exactly 60 lines.
	for len(lines) < 58 {
		lines = append(lines, `{"type":"message_update","message":{"role":"assistant","content":[{"type":"text","text":"tok"}]}}`)
	}
	lines = append(lines,
		`{"type":"agent_end","turns":2,"reason":"completed"}`,
		`{"type":"advisor_cost_changed","cost_usd":0.0123}`,
	)
	return lines
}

func sampleText(lines []string) string { return strings.Join(lines, "\n") + "\n" }

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func eventType(t *testing.T, line string) string {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("kept line is not valid JSON: %q: %v", line, err)
	}
	typ, _ := obj["type"].(string)
	return typ
}

// TestNdjsonFilterKeepsWhitelistedEvents pins the whole point of the capture
// filter: a 60-line omp stream is compacted to the handful of information-bearing
// events (<=12 lines), every surviving line is still valid JSONL, and the events
// the session capture / web timeline depend on are all present.
func TestNdjsonFilterKeepsWhitelistedEvents(t *testing.T) {
	sample := ompSample()
	if len(sample) != 60 {
		t.Fatalf("fixture is %d lines, want 60", len(sample))
	}

	var out bytes.Buffer
	f := New(&out, Options{Keep: ompKeep})
	if _, err := f.Write([]byte(sampleText(sample))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := splitLines(out.String())
	if len(got) > 12 {
		t.Fatalf("kept %d lines, want <=12 (stream was %d lines)", len(got), len(sample))
	}
	if len(got) != 11 {
		t.Fatalf("kept %d lines, want 11 (%v)", len(got), got)
	}

	want := map[string]int{}
	for _, l := range got {
		want[eventType(t, l)]++
		if strings.Contains(l, "message_update") || strings.Contains(l, "tool_execution_update") {
			t.Fatalf("incremental event survived the filter: %s", l)
		}
	}
	for _, typ := range []string{"session", "tool_execution_start", "tool_execution_end", "message_end", "turn_end"} {
		if want[typ] == 0 {
			t.Fatalf("event %q missing from the filtered stream (kept types: %v)", typ, want)
		}
	}
	if want["turn_end"] != 2 || want["tool_execution_end"] != 2 {
		t.Fatalf("kept types = %v, want two turns' worth of turn_end/tool_execution_end", want)
	}

	kept, dropped := f.Counts()
	if kept != 11 || dropped != 49 {
		t.Fatalf("Counts() = (%d kept, %d dropped), want (11, 49)", kept, dropped)
	}
	if kept+dropped != len(sample) {
		t.Fatalf("kept+dropped = %d, want every one of the %d input lines accounted for", kept+dropped, len(sample))
	}

	// A dotted entry navigates a nested path (e.g. `message.role`): the line is
	// kept when that path holds a non-empty string.
	var dotted bytes.Buffer
	df := New(&dotted, Options{Keep: []string{"result.stdout"}})
	if _, err := df.Write([]byte(sampleText(sample))); err != nil {
		t.Fatalf("Write(dotted): %v", err)
	}
	_ = df.Close()
	dottedTypes := map[string]int{}
	for _, l := range splitLines(dotted.String()) {
		dottedTypes[eventType(t, l)]++
	}
	if dottedTypes["tool_execution_end"] != 2 || dottedTypes["session"] != 1 || len(dottedTypes) != 2 {
		t.Fatalf("dotted whitelist kept types %v, want the 2 tool_execution_end lines plus the session row", dottedTypes)
	}

	// The session row is ALWAYS kept: omp's session capture depends on it, so a
	// whitelist that forgot it must not be able to break resume.
	var sessionOnly bytes.Buffer
	sf := New(&sessionOnly, Options{Keep: []string{"message_end"}})
	if _, err := sf.Write([]byte(sampleText(sample))); err != nil {
		t.Fatalf("Write(session-only): %v", err)
	}
	_ = sf.Close()
	types := map[string]int{}
	for _, l := range splitLines(sessionOnly.String()) {
		types[eventType(t, l)]++
	}
	if types["session"] != 1 || types["message_end"] != 2 || len(types) != 2 {
		t.Fatalf("kept types = %v, want the session row plus message_end", types)
	}
}

// TestNdjsonFilterPassesThroughText: a text agent (and any non-JSON line inside an
// ndjson stream) is never parsed away — the filter is transparent when there is
// nothing structured to compact.
func TestNdjsonFilterPassesThroughText(t *testing.T) {
	text := "I read the file and found 3 problems.\n\n  - missing header\n  - typo in README\n"

	var out bytes.Buffer
	f := New(&out, Options{Keep: ompKeep})
	if _, err := f.Write([]byte(text)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.String() != text {
		t.Fatalf("text output was rewritten:\n got %q\nwant %q", out.String(), text)
	}
	kept, dropped := f.Counts()
	if kept != 4 || dropped != 0 {
		t.Fatalf("Counts() = (%d kept, %d dropped), want (4, 0)", kept, dropped)
	}

	// Mixed stream: a JSON line to be dropped sits between two text lines.
	mixed := "plain one\n" + `{"type":"message_update","delta":"tok"}` + "\nplain two\n"
	var mout bytes.Buffer
	mf := New(&mout, Options{Keep: ompKeep})
	if _, err := mf.Write([]byte(mixed)); err != nil {
		t.Fatalf("Write(mixed): %v", err)
	}
	_ = mf.Close()
	if mout.String() != "plain one\nplain two\n" {
		t.Fatalf("mixed output = %q, want the two text lines only", mout.String())
	}
	if kept, dropped := mf.Counts(); kept != 2 || dropped != 1 {
		t.Fatalf("mixed Counts() = (%d, %d), want (2, 1)", kept, dropped)
	}
}

// TestNdjsonFilterHandlesSplitLinesAndLongLines covers the two ways a naive
// scanner breaks: writes that split a line in half (a pipe read boundary is
// arbitrary), and a single line larger than the parse cap (1MB in production),
// which must be passed through verbatim instead of being parsed or buffered.
func TestNdjsonFilterHandlesSplitLinesAndLongLines(t *testing.T) {
	keep := []string{"session", "message_end"}
	stream := `{"type":"session","id":"abc"}` + "\n" +
		`{"type":"message_update","delta":"x"}` + "\n" +
		`{"type":"message_end","message":{"content":[{"type":"text","text":"done"}]}}` + "\n"

	var out bytes.Buffer
	f := New(&out, Options{Keep: keep})
	// Split every line at an awkward offset, including mid-JSON.
	for i := 0; i < len(stream); i += 7 {
		end := min(i+7, len(stream))
		if _, err := f.Write([]byte(stream[i:end])); err != nil {
			t.Fatalf("Write(%d:%d): %v", i, end, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wantSplit := `{"type":"session","id":"abc"}` + "\n" +
		`{"type":"message_end","message":{"content":[{"type":"text","text":"done"}]}}` + "\n"
	if out.String() != wantSplit {
		t.Fatalf("split-write output:\n got %q\nwant %q", out.String(), wantSplit)
	}

	// A line beyond MaxLineBytes is never parsed: it passes through byte-for-byte
	// (dropping it would silently lose output the filter cannot understand).
	long := `{"type":"message_update","delta":"` + strings.Repeat("A", 4096) + `"}`
	var lout bytes.Buffer
	lf := New(&lout, Options{Keep: keep, MaxLineBytes: 512})
	if _, err := lf.Write([]byte(long + "\n")); err != nil {
		t.Fatalf("Write(long): %v", err)
	}
	if _, err := lf.Write([]byte(`{"type":"message_update","delta":"tok"}` + "\n")); err != nil {
		t.Fatalf("Write(after long): %v", err)
	}
	if err := lf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if lout.String() != long+"\n" {
		t.Fatalf("long-line output = %q, want the oversize line verbatim and nothing else", lout.String())
	}
	if kept, dropped := lf.Counts(); kept != 1 || dropped != 1 {
		t.Fatalf("long-line Counts() = (%d, %d), want (1, 1)", kept, dropped)
	}

	// A trailing line without a newline (the child died mid-write) is still flushed.
	var tout bytes.Buffer
	tf := New(&tout, Options{Keep: keep})
	if _, err := tf.Write([]byte(`{"type":"session","id":"tail"}`)); err != nil {
		t.Fatalf("Write(tail): %v", err)
	}
	if err := tf.Close(); err != nil {
		t.Fatalf("Close(tail): %v", err)
	}
	if tout.String() != `{"type":"session","id":"tail"}` {
		t.Fatalf("tail output = %q, want the unterminated final line", tout.String())
	}
}

// TestNdjsonRawSidecarWhenEnabled: ndjson_raw keeps a verbatim copy of EVERY line
// (dropped ones included) in a sidecar for troubleshooting; without it nothing is
// written anywhere but the compacted stream.
func TestNdjsonRawSidecarWhenEnabled(t *testing.T) {
	lines := ompSample()
	raw := strings.Builder{}

	var out bytes.Buffer
	f := New(&out, Options{Keep: ompKeep, Raw: &raw})
	if _, err := f.Write([]byte(sampleText(lines))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if raw.String() != sampleText(lines) {
		t.Fatalf("raw sidecar is not verbatim: got %d bytes, want %d", len(raw.String()), len(sampleText(lines)))
	}
	if len(splitLines(out.String())) >= len(lines) {
		t.Fatalf("compacted stream kept %d of %d lines", len(splitLines(out.String())), len(lines))
	}

	var out2 bytes.Buffer
	f2 := New(&out2, Options{Keep: ompKeep})
	if _, err := f2.Write([]byte(sampleText(lines))); err != nil {
		t.Fatalf("Write(no raw): %v", err)
	}
	_ = f2.Close()
	if len(splitLines(out2.String())) != len(splitLines(out.String())) {
		t.Fatalf("the raw sidecar changed the compacted stream")
	}
}
