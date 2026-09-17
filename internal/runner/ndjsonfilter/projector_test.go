package ndjsonfilter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// claudeSampleSessionID is the session id of claudeSample().
const claudeSampleSessionID = "9f2b7c14-5d3e-4a61-8f0c-2b6d5e7a1c33"

// claudeSample is a `claude --output-format stream-json --verbose` run: an init
// row, a tool call with its result, the answer, and the terminal `result` row —
// with the per-token stream_event rows that the capture exists to drop.
func claudeSample() []string {
	sid := claudeSampleSessionID
	return []string{
		`{"type":"system","subtype":"init","session_id":"` + sid + `","tools":["Bash","Read","Write"],"model":"claude-sonnet-4","cwd":"D:/work/x"}`,
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls -la"}}]},"session_id":"` + sid + `"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"total 4\ndrwxr-xr-x  3 x"}]},"session_id":"` + sid + `"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"text":"Found"}}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Found 3 problems."}]},"session_id":"` + sid + `"}`,
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":4210,"num_turns":2,"result":"Found 3 problems.","session_id":"` + sid + `","total_cost_usd":0.0123,"usage":{"input_tokens":1200,"output_tokens":80}}`,
	}
}

// project feeds a whole stream through a filter and returns the filter with both
// sinks drained.
func project(t *testing.T, opt Options, lines []string) (*Filter, string, string) {
	t.Helper()
	var stdout, events bytes.Buffer
	f := New(&stdout, &events, opt)
	if _, err := f.Write([]byte(sampleText(lines))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return f, stdout.String(), events.String()
}

// decodeEvent parses one event-stream line.
func decodeEvent(t *testing.T, line string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("event line is not valid JSON: %q: %v", line, err)
	}
	return obj
}

// TestOmpProjectorFinalTextAndTrimmedEvents is the whole point of the projector
// (bd h-aii-525u) on a real-shaped `omp --mode json` stream: stdout gets EXACTLY
// the final assistant text (not one event line, not one per-token fragment), the
// event stream gets one small line per information-bearing event (turn_end
// without its embedded message), the session id is extracted, and the per-token
// noise is gone.
func TestOmpProjectorFinalTextAndTrimmedEvents(t *testing.T) {
	sample := ompSample()
	f, stdout, events := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, sample)

	if want := "turn 2 done\n"; stdout != want {
		t.Fatalf("stdout = %q, want exactly the final assistant text %q", stdout, want)
	}
	if f.SessionID() != ompSampleSessionID {
		t.Fatalf("SessionID() = %q, want %q", f.SessionID(), ompSampleSessionID)
	}

	lines := splitLines(events)
	if len(lines) == 0 {
		t.Fatal("event stream is empty")
	}
	types := map[string]int{}
	var turnEnds []map[string]any
	for _, l := range lines {
		if len(l) > DefaultMaxEventBytes {
			t.Fatalf("event line is %d bytes, want <= %d: %s", len(l), DefaultMaxEventBytes, l)
		}
		if strings.Contains(l, "message_update") || strings.Contains(l, "tool_execution_update") {
			t.Fatalf("per-token event reached the event stream: %s", l)
		}
		obj := decodeEvent(t, l)
		typ := eventType(t, l)
		types[typ]++
		if typ == "turn_end" {
			turnEnds = append(turnEnds, obj)
		}
	}
	for _, typ := range []string{"session", "tool_execution_start", "tool_execution_end", "turn_end", "agent_end"} {
		if types[typ] == 0 {
			t.Fatalf("event %q missing from the event stream (types: %v)", typ, types)
		}
	}
	if types["turn_end"] != 2 || types["tool_execution_end"] != 2 {
		t.Fatalf("event types = %v, want two turns' worth of turn_end/tool_execution_end", types)
	}

	for _, obj := range turnEnds {
		if _, ok := obj["message"]; ok {
			t.Fatalf("turn_end still carries the whole message: %v", obj)
		}
		if obj["model"] != "omp-1" || obj["provider"] != "omp-provider" {
			t.Fatalf("turn_end model/provider = %v/%v, want omp-1/omp-provider", obj["model"], obj["provider"])
		}
		usage, ok := obj["usage"].(map[string]any)
		if !ok || usage["total_tokens"] != float64(120) {
			t.Fatalf("turn_end usage = %v, want the run's token counts", obj["usage"])
		}
	}

	// A tool call is readable from the event stream alone: what ran, why, and a
	// bounded summary of its arguments and result.
	var toolStart, toolEnd map[string]any
	for _, l := range lines {
		switch eventType(t, l) {
		case "tool_execution_start":
			toolStart = decodeEvent(t, l)
		case "tool_execution_end":
			toolEnd = decodeEvent(t, l)
		}
	}
	if toolStart["toolName"] != "bash" || toolStart["intent"] != "list the project root" {
		t.Fatalf("tool_execution_start = %v, want the tool name and intent", toolStart)
	}
	if args, _ := toolStart["args"].(string); !strings.Contains(args, "ls -la") {
		t.Fatalf("tool_execution_start args = %v, want a summary of the arguments", toolStart["args"])
	}
	if toolEnd["ok"] != true {
		t.Fatalf("tool_execution_end ok = %v, want true", toolEnd["ok"])
	}
	if res, _ := toolEnd["result"].(string); !strings.Contains(res, "total 4") {
		t.Fatalf("tool_execution_end result = %v, want a summary of the result", toolEnd["result"])
	}

	kept, dropped := f.Counts()
	if kept+dropped != len(sample) {
		t.Fatalf("kept+dropped = %d, want every one of the %d input lines accounted for", kept+dropped, len(sample))
	}
	if f.Truncated() != 0 {
		t.Fatalf("Truncated() = %d, want 0 (the sample fits the event cap)", f.Truncated())
	}
}

// TestClaudeProjectorResultToStdout pins the claude projection: the `result`
// row's text is the answer on stdout, the event stream keeps the init row, the
// tool calls and their results in summarised form, and stream_event is dropped.
func TestClaudeProjectorResultToStdout(t *testing.T) {
	sample := claudeSample()
	f, stdout, events := project(t, Options{Keep: []string{"system", "assistant", "user", "result"}, Projector: ProjectorClaude}, sample)

	if want := "Found 3 problems.\n"; stdout != want {
		t.Fatalf("stdout = %q, want the result text %q", stdout, want)
	}
	if f.SessionID() != claudeSampleSessionID {
		t.Fatalf("SessionID() = %q, want %q", f.SessionID(), claudeSampleSessionID)
	}

	lines := splitLines(events)
	types := map[string]int{}
	var assistant, user, result, init map[string]any
	for _, l := range lines {
		if strings.Contains(l, "stream_event") {
			t.Fatalf("stream_event reached the event stream: %s", l)
		}
		obj := decodeEvent(t, l)
		typ := eventType(t, l)
		types[typ]++
		switch typ {
		case "system":
			init = obj
		case "assistant":
			assistant = obj
		case "user":
			user = obj
		case "result":
			result = obj
		}
	}
	if init["session_id"] != claudeSampleSessionID || init["subtype"] != "init" || init["tool_count"] != float64(3) {
		t.Fatalf("system event = %v, want the init summary with its tool count", init)
	}
	if types["assistant"] != 2 {
		t.Fatalf("assistant events = %d, want 2 (one tool call, one answer)", types["assistant"])
	}
	// The last assistant row is the answer: its text is NOT in the event stream,
	// only the fact that it carried text (the text itself went to stdout).
	if _, ok := assistant["message"]; ok {
		t.Fatalf("assistant event still carries the whole message: %v", assistant)
	}
	if assistant["text_len"] != float64(len("Found 3 problems.")) {
		t.Fatalf("assistant text_len = %v, want the text length", assistant["text_len"])
	}
	// Either assistant row's tool_use blocks are summarised, not embedded.
	for _, l := range lines {
		if eventType(t, l) != "assistant" {
			continue
		}
		tools, _ := decodeEvent(t, l)["tools"].([]any)
		if len(tools) == 0 {
			continue
		}
		tool, _ := tools[0].(map[string]any)
		if tool["name"] != "Bash" {
			t.Fatalf("tool summary = %v, want the tool name", tool)
		}
		if input, _ := tool["input"].(string); !strings.Contains(input, "ls -la") {
			t.Fatalf("tool summary input = %v, want the summarised arguments", tool["input"])
		}
	}
	if tr, _ := user["tool_result"].(string); !strings.Contains(tr, "total 4") {
		t.Fatalf("user event tool_result = %v, want the summarised result", user["tool_result"])
	}
	if _, ok := result["result"]; ok {
		t.Fatalf("result event still carries the answer text: %v", result)
	}
	for _, key := range []string{"subtype", "usage", "total_cost_usd", "duration_ms", "num_turns"} {
		if _, ok := result[key]; !ok {
			t.Fatalf("result event is missing %q: %v", key, result)
		}
	}

	kept, dropped := f.Counts()
	if kept+dropped != len(sample) {
		t.Fatalf("kept+dropped = %d, want every one of the %d input lines accounted for", kept+dropped, len(sample))
	}
}

// TestGenericProjectorKeepToStderr: an agent with no built-in projector is not
// projected at all — the whitelist is the only filter, the surviving lines land
// in the event stream byte-for-byte, and stdout stays empty (nothing can claim a
// line is the answer without a rule for it).
func TestGenericProjectorKeepToStderr(t *testing.T) {
	sample := ompSample()
	f, stdout, events := project(t, Options{Keep: []string{"session", "message_end"}}, sample)

	if stdout != "" {
		t.Fatalf("stdout = %q, want empty for a generic agent with no ndjson_stdout_path", stdout)
	}
	if f.SessionID() != ompSampleSessionID {
		t.Fatalf("SessionID() = %q, want the session id read from the session row", f.SessionID())
	}
	got := splitLines(events)
	var want []string
	for _, l := range sample {
		var obj map[string]any
		if json.Unmarshal([]byte(l), &obj) != nil {
			continue
		}
		if typ, _ := obj["type"].(string); typ == "session" || typ == "message_end" {
			want = append(want, l)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("event stream has %d lines, want %d:\n%v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("event line %d was rewritten:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// TestProjectorTruncatesLongFields: a big tool result, a big pass-through event
// and a line past the parse cap all leave a bounded, marked event behind — the
// log stays readable and the capture still says what it cut.
func TestProjectorTruncatesLongFields(t *testing.T) {
	big := strings.Repeat("x", 5000)
	lines := []string{
		// Summarised by the projector (300 runes), well under the cap.
		`{"type":"tool_execution_end","toolName":"bash","result":{"stdout":"` + big + `","isError":false}}`,
		// Passed through verbatim, then shortened by the event cap.
		`{"type":"advisor_cost_changed","note":"` + big + `"}`,
	}
	f, stdout, events := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, lines)

	if stdout != "" {
		t.Fatalf("stdout = %q, want empty (no assistant text in this stream)", stdout)
	}
	got := splitLines(events)
	if len(got) != 2 {
		t.Fatalf("event stream has %d lines, want 2", len(got))
	}
	for _, l := range got {
		if len(l) > DefaultMaxEventBytes {
			t.Fatalf("event line is %d bytes, want <= %d", len(l), DefaultMaxEventBytes)
		}
		if !strings.Contains(l, truncationMarker) {
			t.Fatalf("event line was shortened without the marker: %q", l)
		}
		if !json.Valid([]byte(l)) {
			t.Fatalf("shortened event is not valid JSON: %q", l)
		}
	}
	if f.Truncated() != 1 {
		t.Fatalf("Truncated() = %d, want 1 (only the pass-through event hit the cap)", f.Truncated())
	}

	// A line past the parse cap is never parsed: its head is kept (bounded) so the
	// output is still visible, and the line is counted as truncated.
	huge := `{"type":"advisor_cost_changed","note":"` + big + `"}`
	var stdout2, events2 bytes.Buffer
	hf := New(&stdout2, &events2, Options{Keep: ompKeep, Projector: ProjectorOMP, MaxLineBytes: 512, MaxEventBytes: 256})
	if _, err := hf.Write([]byte(huge + "\n")); err != nil {
		t.Fatalf("Write(huge): %v", err)
	}
	if err := hf.Close(); err != nil {
		t.Fatalf("Close(huge): %v", err)
	}
	head := strings.TrimSuffix(events2.String(), "\n")
	if len(head) > 256 {
		t.Fatalf("oversize line head is %d bytes, want <= 256", len(head))
	}
	if !strings.HasPrefix(huge, strings.TrimSuffix(head, truncationMarker)) {
		t.Fatalf("oversize line head %q is not a prefix of the line", head)
	}
	if !strings.Contains(head, truncationMarker) {
		t.Fatalf("oversize line head was cut without the marker: %q", head)
	}
	if hf.Truncated() != 1 || stdout2.Len() != 0 {
		t.Fatalf("oversize line: Truncated() = %d, stdout = %q; want 1 and empty", hf.Truncated(), stdout2.String())
	}
}

// TestNdjsonStdoutPathOverride: ndjson_stdout_path names where the answer lives
// for an agent the built-in projectors do not know, and it overrides their own
// final-text rule when both could apply.
func TestNdjsonStdoutPathOverride(t *testing.T) {
	// A generic agent (no built-in projector) with its own answer path.
	lines := []string{
		`{"type":"chunk","data":{"answer":"the final answer"}}`,
		`{"type":"chunk","data":{"answer":"a later one wins the line, both are written"}}`,
		`{"type":"stats","tokens":42}`,
	}
	_, stdout, _ := project(t, Options{Keep: []string{"chunk", "stats"}, StdoutPath: "data.answer"}, lines)
	want := "the final answer\na later one wins the line, both are written\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want the configured path's values %q", stdout, want)
	}

	// The same knob on an agent whose projector already has a final-text rule: the
	// configured path wins over the built-in one.
	ompLines := []string{
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"built-in answer"}]}}`,
		`{"type":"done","output":"answer from the path"}`,
	}
	_, stdout, _ = project(t, Options{Keep: []string{"message_end", "done"}, Projector: ProjectorOMP, StdoutPath: "output"}, ompLines)
	if want := "answer from the path\n"; stdout != want {
		t.Fatalf("stdout = %q, want the configured path's value %q", stdout, want)
	}
}
