package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	acpproto "github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func readRunnerACPEvents(t *testing.T, resultDir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(resultDir, "artifacts", ACPFileName))
	if err != nil {
		t.Fatalf("read acp.jsonl: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode acp.jsonl line %q: %v", line, err)
		}
		out = append(out, event)
	}
	return out
}

func TestACPRunnerRecordsPromptAndMessages(t *testing.T) {
	if messageFlushInterval != 2*time.Second {
		t.Fatalf("messageFlushInterval = %s, want 2s", messageFlushInterval)
	}
	oldInterval := messageFlushInterval
	messageFlushInterval = 10 * time.Millisecond
	t.Cleanup(func() { messageFlushInterval = oldInterval })

	resultDir := t.TempDir()
	prompt := strings.Repeat("界", 8001)
	var stdout bytes.Buffer
	res := (&Runner{}).Run(context.Background(), runner.Request{
		JobID:   "job-w3a-runner",
		Command: testcmd.Path(t),
		Args: acptest.CmdArgs(acptest.Options{
			MessagePause: 100 * time.Millisecond,
		}),
		WorkDir: t.TempDir(),
		Stdout:  &stdout,
		Stderr:  io.Discard,
		ACP: &runner.ACPRequest{
			Prompt:      prompt,
			ResultDir:   resultDir,
			LogThoughts: true,
		},
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: exit=%d err=%v", res.ExitCode, res.Err)
	}

	events := readRunnerACPEvents(t, resultDir)
	var business []map[string]any
	for _, event := range events {
		switch event["t"] {
		case "session", "set_mode", "mode":
			continue
		default:
			business = append(business, event)
		}
	}
	if len(business) == 0 || business[0]["t"] != "prompt" {
		t.Fatalf("first business event = %v, want prompt", business)
	}
	gotPrompt, _ := business[0]["text"].(string)
	if utf8.RuneCountInString(gotPrompt) != 8000 || business[0]["truncated"] != true {
		t.Fatalf("prompt event = %#v, want 8000 runes and truncated=true", business[0])
	}

	var messages []string
	messageBeforeIdleMarker := false
	stopIndex, lastMessageIndex := -1, -1
	for i, event := range business {
		switch event["t"] {
		case "message":
			text, _ := event["text"].(string)
			messages = append(messages, text)
			lastMessageIndex = i
		case "available_commands_update":
			messageBeforeIdleMarker = lastMessageIndex >= 0 && lastMessageIndex < i
		case "stop":
			stopIndex = i
		case "tool_call":
			if event["status"] == "pending" {
				locations, _ := event["locations"].([]any)
				if len(locations) != 1 {
					t.Fatalf("tool locations = %#v, want one structured location", event["locations"])
				}
				location, _ := locations[0].(map[string]any)
				if location["path"] != "main.go" || location["line"] != float64(7) {
					t.Fatalf("tool location = %#v, want main.go:7", location)
				}
			}
		}
	}
	if !messageBeforeIdleMarker {
		t.Fatalf("idle message was not persisted before the marker: %#v", business)
	}
	if len(messages) != 2 || messages[0] != acptest.TextHello+acptest.TextWorld || messages[1] != acptest.TextDone {
		t.Fatalf("message records = %#v, want the two scripted assistant blocks", messages)
	}
	wantStdout := strings.Join(messages, "\n\n") + "\n"
	if stdout.String() != wantStdout {
		t.Fatalf("stdout = %q, want message-equivalent %q", stdout.String(), wantStdout)
	}
	if stopIndex < 0 || lastMessageIndex < 0 || lastMessageIndex >= stopIndex {
		t.Fatalf("last message index=%d stop index=%d, want residual message before stop", lastMessageIndex, stopIndex)
	}
}

func TestACPToolCallContentRefreshReachesArtifactAndStderr(t *testing.T) {
	content := json.RawMessage(`[{"type":"diff","oldText":"old value","newText":"new value"}]`)
	location := func(path string) []acpproto.ToolCallLocation {
		return []acpproto.ToolCallLocation{{Path: path}}
	}
	var stderr bytes.Buffer
	h := &handler{stderr: &stderr, toolStatus: map[string]string{}}
	first := &acpproto.ToolCall{
		ToolCallID: "tool-1", Title: "Edit file", Kind: "edit", Status: "in_progress",
		Content: content, Locations: location("old.go"),
	}
	refresh := &acpproto.ToolCall{
		ToolCallID: "tool-1", Title: "Edit file", Kind: "edit", Status: "in_progress",
		Content: content, Locations: location("new.go"),
	}
	h.recordToolCall(first)
	h.recordToolCall(refresh)
	h.recordToolCall(refresh) // identical refresh remains coalesced

	event := toolCallEvent(refresh)
	resultDir := t.TempDir()
	writer, err := openEventWriter(resultDir, false)
	if err != nil {
		t.Fatal(err)
	}
	writer.write(event)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	artifactEvents := readRunnerACPEvents(t, resultDir)
	if len(artifactEvents) != 1 || artifactEvents[0]["content"] == nil {
		t.Fatalf("artifact tool event = %#v, want diff content", artifactEvents)
	}
	if got, ok := event["content"].(json.RawMessage); !ok || string(got) != string(content) {
		t.Fatalf("artifact content = %#v, want preserved diff JSON %s", event["content"], content)
	}
	if got := strings.Count(strings.TrimSpace(stderr.String()), "\n") + 1; got != 2 {
		t.Fatalf("stderr event count = %d, want initial call and changed refresh", got)
	}
	if !strings.Contains(stderr.String(), "oldText") || !strings.Contains(stderr.String(), "newText") || !strings.Contains(stderr.String(), "new.go") {
		t.Fatalf("stderr omitted diff content or refreshed path: %s", stderr.String())
	}
	if h.toolCalls != 1 {
		t.Fatalf("tool call tally = %d, want one call across refreshes", h.toolCalls)
	}
}

func TestBoundedToolContentCapsOversizePayload(t *testing.T) {
	tooLarge := json.RawMessage(`"` + strings.Repeat("x", maxToolContentBytes) + `"`)
	got, truncated := boundedToolContent(tooLarge)
	if len(got) != 0 || !truncated {
		t.Fatalf("bounded content = %d bytes, truncated=%v; want omitted content marked truncated", len(got), truncated)
	}
}
