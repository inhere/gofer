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
