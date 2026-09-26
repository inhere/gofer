package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

type testACPFlusher struct{}

func (testACPFlusher) Flush() {}

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
