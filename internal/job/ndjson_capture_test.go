package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

const ndjsonSampleSessionID = "0f9c1e2a-1111-4a2b-8c3d-9e8f7a6b5c4d"

// ndjsonSample is a trimmed `omp --mode json` stream: the session row, one tool
// call and one assistant turn, drowned in the per-token message_update /
// tool_execution_update events that the capture filter exists to drop.
func ndjsonSample() []string {
	lines := []string{
		`{"type":"session","id":"` + ndjsonSampleSessionID + `","model":"omp-1","cwd":"D:/work/x"}`,
		`{"type":"turn_start","turn":1}`,
		`{"type":"message_start","turn":1,"message":{"role":"assistant","content":[]}}`,
	}
	for i := 0; i < 6; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"message_update","seq":%d,"message":{"role":"assistant","content":[{"type":"text","text":"tok"}]}}`, i))
	}
	lines = append(lines,
		`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"bash","intent":"list the project root","args":{"command":"ls -la"}}`,
		`{"type":"tool_execution_update","toolCallId":"call_1","partialOutput":"chunk1"}`,
		`{"type":"tool_execution_update","toolCallId":"call_1","partialOutput":"chunk2"}`,
		`{"type":"tool_execution_end","toolCallId":"call_1","toolName":"bash","result":{"stdout":"total 4","isError":false}}`,
		`{"type":"message_end","turn":1,"message":{"role":"assistant","content":[{"type":"text","text":"listed the root"}]},"usage":{"input_tokens":100,"output_tokens":20}}`,
		`{"type":"turn_end","turn":1,"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120},"model":"omp-1"}`,
		`{"type":"agent_end","turns":1,"reason":"completed"}`,
	)
	return lines
}

// TestJobRunNdjsonAgentStdoutIsCompact is the end-to-end proof of the capture-time
// projection (bd h-aii-rpky / bd h-aii-525u): an `output_format: ndjson`
// cli-agent writing an omp-style event stream lands the agent's FINAL answer in
// stdout.log (one line of text, no JSON at all), the compact events in stderr.log
// (incremental tokens gone), the session id on the result, and the kept/dropped
// counts recorded and persisted.
func TestJobRunNdjsonAgentStdoutIsCompact(t *testing.T) {
	root := t.TempDir()
	sample := ndjsonSample()
	samplePath := filepath.Join(root, "omp-sample.ndjson")
	if err := os.WriteFile(samplePath, []byte(strings.Join(sample, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"omp"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			// Keyed "omp" so the built-in omp whitelist + projector + session capture
			// apply; the command merely replays the sample fixture.
			"omp": {
				Type:         agent.TypeCLIAgent,
				Command:      testcmd.Path(t),
				Args:         []string{"cat-file", samplePath},
				OutputFormat: config.OutputFormatNDJSON,
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "omp", Runner: "local",
		Prompt: "list the project root", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}

	// stdout.log is the ANSWER: the last assistant message, and nothing else.
	stdout, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(final.ID, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("read stdout.log: %v", err)
	}
	if got, want := string(stdout), "listed the root\n"; got != want {
		t.Fatalf("stdout.log = %q, want exactly the final assistant text %q", got, want)
	}

	// stderr.log is the EVENT STREAM: one compact JSON line per information-bearing
	// event, no per-token noise, no final-text row.
	stderr, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(final.ID, store.StreamStderr, 0)
	if err != nil {
		t.Fatalf("read stderr.log: %v", err)
	}
	kept := splitLogLines(string(stderr))
	if len(kept) != 5 {
		t.Fatalf("stderr.log has %d lines, want the 5 compact events:\n%s", len(kept), stderr)
	}
	types := map[string]int{}
	for _, l := range kept {
		var obj map[string]any
		if err := json.Unmarshal([]byte(l), &obj); err != nil {
			t.Fatalf("stderr.log line is not valid JSON: %q: %v", l, err)
		}
		typ, _ := obj["type"].(string)
		types[typ]++
		if typ == "message_update" || typ == "tool_execution_update" || typ == "message_start" || typ == "turn_start" {
			t.Fatalf("incremental event reached stderr.log: %s", l)
		}
	}
	for _, typ := range []string{"session", "tool_execution_start", "tool_execution_end", "turn_end", "agent_end"} {
		if types[typ] != 1 {
			t.Fatalf("stderr.log is missing the %q event (kept types: %v)", typ, types)
		}
	}

	// The session row was projected, so capture finds the session id.
	if final.SessionID != ndjsonSampleSessionID {
		t.Fatalf("session_id = %q, want %q (the projected session row must be read)", final.SessionID, ndjsonSampleSessionID)
	}
	if final.NDJSONKept != 6 || final.NDJSONDropped != len(sample)-6 {
		t.Fatalf("ndjson kept/dropped = %d/%d, want 6/%d", final.NDJSONKept, final.NDJSONDropped, len(sample)-6)
	}
	if final.NDJSONTruncated != 0 {
		t.Fatalf("ndjson truncated = %d, want 0 (the sample fits the event cap)", final.NDJSONTruncated)
	}

	// Evicted from memory by now: this read goes through the metadata store, so it
	// also proves the counts are persisted, not just held on the live entry.
	persisted, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("job %s not found in the store", final.ID)
	}
	if persisted.NDJSONKept != final.NDJSONKept || persisted.NDJSONDropped != final.NDJSONDropped {
		t.Fatalf("persisted counts = %d/%d, want %d/%d",
			persisted.NDJSONKept, persisted.NDJSONDropped, final.NDJSONKept, final.NDJSONDropped)
	}
	if persisted.SessionID != final.SessionID {
		t.Fatalf("persisted session_id = %q, want %q", persisted.SessionID, final.SessionID)
	}
}

// splitLogLines splits a log tail into its non-empty lines.
func splitLogLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestJobRunTextAgentStdoutIsUntouched guards the other half of the contract: a
// text agent (the default) keeps every byte, so the filter can never surprise an
// agent that was not configured for structured output.
func TestJobRunTextAgentStdoutIsUntouched(t *testing.T) {
	root := t.TempDir()
	samplePath := filepath.Join(root, "omp-sample.ndjson")
	if err := os.WriteFile(samplePath, []byte(strings.Join(ndjsonSample(), "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"omp"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			"omp": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"cat-file", samplePath}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "omp", Runner: "local",
		Prompt: "list the project root", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(final.ID, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("read stdout.log: %v", err)
	}
	if got, want := string(out), strings.Join(ndjsonSample(), "\n")+"\n"; got != want {
		t.Fatalf("text agent stdout.log was rewritten:\n got %q\nwant %q", got, want)
	}
	if final.NDJSONKept != 0 || final.NDJSONDropped != 0 {
		t.Fatalf("text agent recorded ndjson counts %d/%d, want 0/0", final.NDJSONKept, final.NDJSONDropped)
	}
}
