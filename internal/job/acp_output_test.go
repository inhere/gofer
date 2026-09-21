package job

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
)

// acpStderr returns a job's stderr.log: the agent's own diagnostics AND the compact
// event lines the runner projects into it (bd h-aii-rnxk).
func acpStderr(t *testing.T, root, jobID string) string {
	t.Helper()
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(jobID, store.StreamStderr, 0)
	if err != nil {
		t.Fatalf("read stderr.log: %v", err)
	}
	return string(out)
}

// acpStderrEvents returns the compact NDJSON event lines of a stderr log, in order,
// skipping the agent's non-JSON diagnostics.
func acpStderrEvents(t *testing.T, stderr string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("stderr line is not valid JSON: %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

// acpStderrOfType filters the stderr events by `type`.
func acpStderrOfType(t *testing.T, stderr, typ string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range acpStderrEvents(t, stderr) {
		if ev["type"] == typ {
			out = append(out, ev)
		}
	}
	return out
}

// TestACPStdoutSeparatesMessages is bd h-aii-7kja ①: stdout.log is what a human reads,
// so the agent's text must not run together across a tool call — each message block ends
// with a newline, and a blank line separates the text before a tool call from the text
// after it.
func TestACPStdoutSeparatesMessages(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(final.ID, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("read stdout.log: %v", err)
	}
	// The script writes "Hello"+" world.", then a tool call (three statuses) and a
	// permission round trip, then " Done.".
	want := acptest.TextHello + acptest.TextWorld + "\n\n" + acptest.TextDone + "\n"
	if string(out) != want {
		t.Fatalf("stdout.log = %q, want %q", out, want)
	}
}

// TestACPThoughtsCoalescedToStderr is bd h-aii-7kja ②: a per-token thought stream is
// coalesced into ONE stderr event line and ONE acp.jsonl line instead of flooding both
// (a 60k-line acp.jsonl per job, 96% of it thoughts).
func TestACPThoughtsCoalescedToStderr(t *testing.T) {
	root := t.TempDir()
	const shards = 20
	s := newACPService(t, root, acptest.Options{ThoughtChunks: shards})

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	want := strings.Repeat(acptest.TextThink, shards)

	thoughts := acpStderrOfType(t, acpStderr(t, root, final.ID), "thought")
	if len(thoughts) != 1 {
		t.Fatalf("stderr thought lines = %d, want 1 (events=%v)", len(thoughts), thoughts)
	}
	if got, _ := thoughts[0]["text"].(string); got != want {
		t.Fatalf("stderr thought text = %q, want the %d shards merged", got, shards)
	}

	var jsonlThoughts []map[string]any
	for _, l := range readACPJSONL(t, final.ResultDir) {
		if l["t"] == "thought" {
			jsonlThoughts = append(jsonlThoughts, l)
		}
	}
	if len(jsonlThoughts) != 1 {
		t.Fatalf("acp.jsonl thought lines = %d, want 1", len(jsonlThoughts))
	}
	if got, _ := jsonlThoughts[0]["text"].(string); got != want {
		t.Fatalf("acp.jsonl thought text = %q, want the %d shards merged", got, shards)
	}
}

// TestACPToolCallsToStderrNotTimeline is bd h-aii-rnxk: the execution details (tool
// calls) belong on stderr, where NdjsonTimeline and `job logs stderr` render them; the
// job event timeline keeps lifecycle only — no per-tool-call rows, one turn summary.
func TestACPToolCallsToStderrNotTimeline(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}

	toolCalls := acpStderrOfType(t, acpStderr(t, root, final.ID), "tool_call")
	if len(toolCalls) != 3 {
		t.Fatalf("stderr tool_call lines = %d, want the 3 status changes (%v)", len(toolCalls), toolCalls)
	}
	first := toolCalls[0]
	if first["id"] != acptest.ToolCallID || first["title"] != acptest.ToolTitle || first["kind"] != acptest.ToolKind {
		t.Fatalf("stderr tool_call = %v, want id/title/kind of the scripted call", first)
	}
	if got := []any{toolCalls[0]["status"], toolCalls[1]["status"], toolCalls[2]["status"]}; got[0] != "pending" || got[1] != "in_progress" || got[2] != "completed" {
		t.Fatalf("stderr tool_call statuses = %v, want pending,in_progress,completed", got)
	}

	events, err := s.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var summaries []map[string]any
	for _, e := range events {
		if e.Type == "job.tool_call" {
			t.Fatalf("job.tool_call is still recorded on the timeline: %+v", e)
		}
		if e.Type == EventJobACPSummary {
			var d map[string]any
			if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
				t.Fatalf("summary detail is not JSON: %q", e.Detail)
			}
			summaries = append(summaries, d)
		}
	}
	if len(summaries) != 1 {
		t.Fatalf("job.acp_summary events = %d, want exactly 1 (events=%v)", len(summaries), events)
	}
	d := summaries[0]
	// One tool call (three status updates), one thought shard, one permission ask, and
	// the stop reason the turn ended with.
	if d["tool_calls"] != float64(1) || d["thoughts"] != float64(1) || d["permissions"] != float64(1) {
		t.Fatalf("summary counts = %v, want tool_calls=1 thoughts=1 permissions=1", d)
	}
	if d["stop_reason"] != "end_turn" {
		t.Fatalf("summary stop_reason = %v, want end_turn", d["stop_reason"])
	}
}

// TestACPLogThoughtsOff: `agents.<k>.acp.log_thoughts: false` drops the coalesced
// thought everywhere — it is the operator's answer to "my logs are full of the agent
// thinking out loud".
func TestACPLogThoughtsOff(t *testing.T) {
	root := t.TempDir()
	off := false
	s := newACPServiceAgent(t, root, acptest.Options{}, nil, func(ac *config.AgentConfig) {
		ac.ACP = &config.ACPConfig{LogThoughts: &off}
	})

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if got := acpStderrOfType(t, acpStderr(t, root, final.ID), "thought"); len(got) != 0 {
		t.Fatalf("stderr kept thoughts although acp.log_thoughts=false: %v", got)
	}
	for _, l := range readACPJSONL(t, final.ResultDir) {
		if l["t"] == "thought" {
			t.Fatalf("acp.jsonl kept a thought although acp.log_thoughts=false: %v", l)
		}
	}
}
