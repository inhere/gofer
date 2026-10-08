package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
)

type testACPFlusher struct{}

func (testACPFlusher) Flush() {}

type lockedACPWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedACPWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedACPWriter) Flush() {}

func (w *lockedACPWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestACPStreamCapsOversizeText(t *testing.T) {
	oldCap := MaxSSEFrameBytes
	MaxSSEFrameBytes = 160
	defer func() { MaxSSEFrameBytes = oldCap }()

	resultDir := t.TempDir()
	artifacts := filepath.Join(resultDir, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"t": "message", "text": strings.Repeat("界", 400)}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "acp.jsonl"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	StreamACP(context.Background(), &out, testACPFlusher{}, nil, "job-oversize", job.JobResult{
		ID: "job-oversize", Status: job.StatusDone, ResultDir: resultDir,
	}, false, ACPStreamOpts{})

	var sawMessage, sawEnd bool
	for _, block := range strings.Split(out.String(), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) < 2 {
			continue
		}
		if lines[0] == "event: end" {
			sawEnd = true
			continue
		}
		if lines[0] != "event: acp" || !strings.HasPrefix(lines[1], "data: ") {
			continue
		}
		payload := strings.TrimPrefix(lines[1], "data: ")
		if len(payload) > MaxSSEFrameBytes {
			t.Fatalf("ACP payload bytes = %d, cap = %d", len(payload), MaxSSEFrameBytes)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatal(err)
		}
		if event["kind"] == "message" {
			sawMessage = true
			if event["truncated"] != true {
				t.Fatalf("oversize message = %#v, want truncated=true", event)
			}
		}
	}
	if !sawMessage || !sawEnd {
		t.Fatalf("stream missing message/end: %s", out.String())
	}
}

func TestACPStreamPreservesToolDiffContentAndLocations(t *testing.T) {
	resultDir := t.TempDir()
	artifacts := filepath.Join(resultDir, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"t": "tool_call_update", "tool_call_id": "tool-1", "title": "Edit file", "status": "completed",
		"content":   json.RawMessage(`[{"type":"diff","oldText":"old value","newText":"new value"}]`),
		"locations": []map[string]any{{"path": "edited.go", "line": 7}},
	}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "acp.jsonl"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	StreamACP(context.Background(), &out, testACPFlusher{}, nil, "job-tool-content", job.JobResult{
		ID: "job-tool-content", Status: job.StatusDone, ResultDir: resultDir,
	}, false, ACPStreamOpts{})
	for _, want := range []string{`"kind":"tool"`, `"oldText":"old value"`, `"newText":"new value"`, `"path":"edited.go"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("ACP stream missing %s: %s", want, out.String())
		}
	}
}

func TestACPStreamCapsOversizeToolContent(t *testing.T) {
	oldCap := MaxSSEFrameBytes
	MaxSSEFrameBytes = 160
	defer func() { MaxSSEFrameBytes = oldCap }()

	resultDir := t.TempDir()
	artifacts := filepath.Join(resultDir, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `[{"type":"diff","oldText":"` + strings.Repeat("x", 400) + `","newText":"new"}]`
	record := map[string]any{"t": "tool_call_update", "tool_call_id": "tool-1", "content": json.RawMessage(content)}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "acp.jsonl"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	StreamACP(context.Background(), &out, testACPFlusher{}, nil, "job-large-tool-content", job.JobResult{
		ID: "job-large-tool-content", Status: job.StatusDone, ResultDir: resultDir,
	}, false, ACPStreamOpts{})
	var sawTruncated bool
	for _, block := range strings.Split(out.String(), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) < 2 || lines[0] != "event: acp" || !strings.HasPrefix(lines[1], "data: ") {
			continue
		}
		payload := strings.TrimPrefix(lines[1], "data: ")
		if len(payload) > MaxSSEFrameBytes {
			t.Fatalf("ACP payload bytes = %d, cap = %d", len(payload), MaxSSEFrameBytes)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatal(err)
		}
		if event["kind"] == "tool" {
			sawTruncated = event["truncated"] == true
		}
	}
	if !sawTruncated {
		t.Fatalf("oversize tool content was not marked truncated: %s", out.String())
	}
}

func TestACPStreamFollowsLiveFile(t *testing.T) {
	resultDir := t.TempDir()
	artifacts := filepath.Join(resultDir, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(artifacts, "acp.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &lockedACPWriter{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		StreamACP(ctx, writer, writer, nil, "job-live", job.JobResult{
			ID: "job-live", Status: job.StatusRunning, ResultDir: resultDir,
		}, true, ACPStreamOpts{})
	}()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not-json\n{\"t\":\"message\",\"text\":\"hel"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * StreamPollInterval)
	if strings.Contains(writer.String(), `"kind":"message"`) {
		t.Fatalf("partial JSONL line emitted early: %s", writer.String())
	}
	if _, err := f.WriteString("lo\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(writer.String(), `"text":"hello"`) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(writer.String(), `"text":"hello"`) {
		t.Fatalf("live append was not emitted: %s", writer.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StreamACP did not stop after context cancellation")
	}
}
