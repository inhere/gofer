package ndjsonfilter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ompSampleSessionID is the session id of ompSample().
const ompSampleSessionID = "0f9c1e2a-1111-4a2b-8c3d-9e8f7a6b5c4d"

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
		`{"type":"session","id":"` + ompSampleSessionID + `","model":"omp-1","cwd":"D:/work/x"}`,
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
			fmt.Sprintf(`{"type":"turn_end","turn":%d,"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120},"model":"omp-1","provider":"omp-provider"}`, turn),
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

// TestNdjsonFilterKeepsWhitelistedEvents pins the compaction half of the capture
// filter: a 60-line omp stream is compacted to the handful of information-bearing
// events, every surviving line is still valid JSONL, the events the session
// capture / web timeline depend on are all present, and the per-token noise is
// gone from BOTH streams.
func TestNdjsonFilterKeepsWhitelistedEvents(t *testing.T) {
	sample := ompSample()
	if len(sample) != 60 {
		t.Fatalf("fixture is %d lines, want 60", len(sample))
	}

	f, stdout, events := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, sample)
	if stdout != "turn 2 done\n" {
		t.Fatalf("stdout = %q, want the run's final assistant text", stdout)
	}

	got := splitLines(events)
	if len(got) > 12 {
		t.Fatalf("kept %d event lines, want <=12 (stream was %d lines)", len(got), len(sample))
	}
	if len(got) != 9 {
		t.Fatalf("kept %d event lines, want 9 (%v)", len(got), got)
	}
	want := map[string]int{}
	for _, l := range got {
		want[eventType(t, l)]++
		if strings.Contains(l, "message_update") || strings.Contains(l, "tool_execution_update") {
			t.Fatalf("incremental event survived the filter: %s", l)
		}
	}
	for _, typ := range []string{"session", "tool_execution_start", "tool_execution_end", "turn_end"} {
		if want[typ] == 0 {
			t.Fatalf("event %q missing from the event stream (kept types: %v)", typ, want)
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
	var dotted, dottedOut bytes.Buffer
	df := New(&dottedOut, &dotted, Options{Keep: []string{"result.stdout"}, Projector: ProjectorOMP})
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

	// The session row is ALWAYS read: omp's session capture depends on it, so a
	// whitelist that forgot it must not be able to break resume.
	var sessionOnly, sessionOut bytes.Buffer
	sf := New(&sessionOut, &sessionOnly, Options{Keep: []string{"message_end"}, Projector: ProjectorOMP})
	if _, err := sf.Write([]byte(sampleText(sample))); err != nil {
		t.Fatalf("Write(session-only): %v", err)
	}
	_ = sf.Close()
	types := map[string]int{}
	for _, l := range splitLines(sessionOnly.String()) {
		types[eventType(t, l)]++
	}
	if types["session"] != 1 || len(types) != 1 {
		t.Fatalf("kept types = %v, want the session row alone", types)
	}
	if sf.SessionID() != ompSampleSessionID {
		t.Fatalf("SessionID() = %q, want %q even when the whitelist omits the row", sf.SessionID(), ompSampleSessionID)
	}
}

// TestNdjsonFilterPassesThroughText: output the capture cannot parse is never
// discarded — a stray text line inside an ndjson stream (a warning the agent
// printed on stdout, say) lands in the event stream with its own spacing intact.
func TestNdjsonFilterPassesThroughText(t *testing.T) {
	text := "I read the file and found 3 problems.\n\n  - missing header\n  - typo in README\n"

	f, stdout, events := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, splitLines(text))
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty (a non-JSON line is not an answer)", stdout)
	}
	wantEvents := "I read the file and found 3 problems.\n  - missing header\n  - typo in README\n"
	if events != wantEvents {
		t.Fatalf("event stream = %q, want the text lines (blank line dropped) %q", events, wantEvents)
	}
	kept, dropped := f.Counts()
	if kept != 3 || dropped != 1 {
		t.Fatalf("Counts() = (%d kept, %d dropped), want (3, 1): three text lines and the blank one", kept, dropped)
	}

	// Mixed stream: a JSON line to be dropped sits between two text lines.
	mixed := "plain one\n" + `{"type":"message_update","delta":"tok"}` + "\nplain two\n"
	_, stdout, events = project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, splitLines(mixed))
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if events != "plain one\nplain two\n" {
		t.Fatalf("mixed event stream = %q, want the two text lines only", events)
	}
}

// TestNdjsonFilterHandlesSplitLinesAndLongLines covers the two ways a naive
// scanner breaks: writes that split a line in half (a pipe read boundary is
// arbitrary), and a single line larger than the parse cap, which must never be
// buffered whole — its head is kept and the rest skipped.
func TestNdjsonFilterHandlesSplitLinesAndLongLines(t *testing.T) {
	keep := []string{"session", "message_end"}
	stream := `{"type":"session","id":"abc"}` + "\n" +
		`{"type":"message_update","delta":"x"}` + "\n" +
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"

	var stdout, events bytes.Buffer
	f := New(&stdout, &events, Options{Keep: keep, Projector: ProjectorOMP})
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
	if want := `{"type":"session","id":"abc"}` + "\n"; events.String() != want {
		t.Fatalf("split-write event stream:\n got %q\nwant %q", events.String(), want)
	}
	if stdout.String() != "done\n" {
		t.Fatalf("split-write stdout = %q, want the final text", stdout.String())
	}

	// A line beyond MaxLineBytes is never parsed: its head (bounded by the event
	// cap) reaches the event stream, and the next line is still handled normally.
	long := `{"type":"message_update","delta":"` + strings.Repeat("A", 4096) + `"}`
	stdout.Reset()
	events.Reset()
	lf := New(&stdout, &events, Options{Keep: keep, Projector: ProjectorOMP, MaxLineBytes: 512})
	if _, err := lf.Write([]byte(long + "\n")); err != nil {
		t.Fatalf("Write(long): %v", err)
	}
	if _, err := lf.Write([]byte(`{"type":"message_update","delta":"tok"}` + "\n")); err != nil {
		t.Fatalf("Write(after long): %v", err)
	}
	if err := lf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	head := strings.TrimSuffix(events.String(), "\n")
	if len(head) > DefaultMaxEventBytes {
		t.Fatalf("oversize line head is %d bytes, want <= %d", len(head), DefaultMaxEventBytes)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(head, truncationMarker)) {
		t.Fatalf("oversize line head is not a prefix of the line: %q", head)
	}
	if !strings.Contains(head, truncationMarker) {
		t.Fatalf("oversize line head was cut without the marker: %q", head)
	}
	if stdout.Len() != 0 {
		t.Fatalf("oversize stdout = %q, want empty", stdout.String())
	}
	if kept, dropped := lf.Counts(); kept != 1 || dropped != 1 {
		t.Fatalf("long-line Counts() = (%d, %d), want (1, 1)", kept, dropped)
	}

	// A trailing line without a newline (the child died mid-write) is still
	// projected, so the tail of the stream is never lost.
	var tout, textOut bytes.Buffer
	tf := New(&textOut, &tout, Options{Keep: keep, Projector: ProjectorOMP})
	if _, err := tf.Write([]byte(`{"type":"session","id":"tail"}`)); err != nil {
		t.Fatalf("Write(tail): %v", err)
	}
	if err := tf.Close(); err != nil {
		t.Fatalf("Close(tail): %v", err)
	}
	if tout.String() != `{"type":"session","id":"tail"}`+"\n" {
		t.Fatalf("tail event stream = %q, want the unterminated final line", tout.String())
	}
	if tf.SessionID() != "tail" {
		t.Fatalf("SessionID() = %q, want %q", tf.SessionID(), "tail")
	}
}

// TestNdjsonRawSidecarWhenEnabled: ndjson_raw keeps a verbatim copy of EVERY line
// (dropped ones and projections included) in a sidecar for troubleshooting;
// without it nothing is written anywhere but the two projected streams.
func TestNdjsonRawSidecarWhenEnabled(t *testing.T) {
	lines := ompSample()
	raw := strings.Builder{}

	f, stdout, events := project(t, Options{Keep: ompKeep, Raw: &raw, Projector: ProjectorOMP}, lines)
	if raw.String() != sampleText(lines) {
		t.Fatalf("raw sidecar is not verbatim: got %d bytes, want %d", len(raw.String()), len(sampleText(lines)))
	}
	if stdout != "turn 2 done\n" {
		t.Fatalf("stdout = %q, want the final assistant text", stdout)
	}
	if len(splitLines(events)) >= len(lines) {
		t.Fatalf("compacted event stream kept %d of %d lines", len(splitLines(events)), len(lines))
	}
	if _, _, again := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, lines); again != events {
		t.Fatalf("the sidecar changed the projected stream:\n got %q\nwant %q", again, events)
	}
	if kept, _ := f.Counts(); kept == 0 {
		t.Fatal("Counts() = 0 kept, want the projected lines counted")
	}
}

// TestNdjsonFilterRoutesStreams covers the two config switches a caller can flip:
// events back onto stdout (ndjson_events_to: stdout) and the pre-525u capture
// where stdout IS the event stream (ndjson_stdout: events).
func TestNdjsonFilterRoutesStreams(t *testing.T) {
	sample := ompSample()

	// ndjson_events_to: stdout — the events go where they always did, and the
	// final text is still appended to stdout once the run ends.
	var out, errOut bytes.Buffer
	f := New(&out, &errOut, Options{Keep: ompKeep, Projector: ProjectorOMP, EventsToStdout: true})
	if _, err := f.Write([]byte(sampleText(sample))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("event writer received %q, want nothing when the events go to stdout", errOut.String())
	}
	if !strings.Contains(out.String(), `"type":"turn_end"`) {
		t.Fatalf("stdout carries no event line:\n%s", out.String())
	}
	if !strings.HasSuffix(out.String(), "turn 2 done\n") {
		t.Fatalf("stdout does not end with the final text:\n%s", out.String())
	}

	// ndjson_stdout: events — stdout is the compact JSONL stream and nothing is
	// extracted as the answer.
	var out2, errOut2 bytes.Buffer
	f2 := New(&out2, &errOut2, Options{Keep: ompKeep, Projector: ProjectorOMP, StdoutEvents: true})
	if _, err := f2.Write([]byte(sampleText(sample))); err != nil {
		t.Fatalf("Write(events): %v", err)
	}
	if err := f2.Close(); err != nil {
		t.Fatalf("Close(events): %v", err)
	}
	if errOut2.Len() != 0 {
		t.Fatalf("stderr received %q, want nothing", errOut2.String())
	}
	if got := len(splitLines(out2.String())); got != 9 {
		t.Fatalf("stdout has %d lines, want the 9 projected events", got)
	}
	if strings.Contains(out2.String(), "turn 2 done") {
		t.Fatalf("stdout carries the extracted answer although ndjson_stdout is events:\n%s", out2.String())
	}
	if kept, dropped := f2.Counts(); kept != 11 || dropped != 49 {
		t.Fatalf("Counts() = (%d, %d), want (11, 49)", kept, dropped)
	}
}
