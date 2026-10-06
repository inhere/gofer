package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// TestTranscriptTailE2E drives the v17 transcript_tail frame over a real hub / worker
// pair: an allowed .jsonl is returned from its end (on a line boundary, size-capped),
// and everything else — outside the allowed roots, not .jsonl, relative, missing — is
// refused by the worker itself.
func TestTranscriptTailE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, _ := startXferWorker(t, ctx)
	// The worker is in-process and reads the roots per request, so the environment is
	// switched after the fixture (which shells out to git) is up.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("GOFER_TRANSCRIPT_ROOTS", "")

	dir := filepath.Join(home, ".claude", "projects", "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := 0; i < 400; i++ {
		sb.WriteString(`{"type":"user","message":{"role":"user","content":"line ` + strings.Repeat("x", 30) + `"}}` + "\n")
	}
	path := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := hub.hub.SendTranscriptTail(ctx, e2eWorkerID, wsproto.TranscriptTail{ReqID: "r1", SessionID: "s1", Path: path, MaxBytes: 2000})
	if err != nil {
		t.Fatalf("SendTranscriptTail: %v", err)
	}
	if !res.OK || !res.Truncated || len(res.Data) == 0 || len(res.Data) > 2000 || !strings.HasPrefix(string(res.Data), `{"type"`) {
		t.Fatalf("tail = ok=%v truncated=%v len=%d err=%q", res.OK, res.Truncated, len(res.Data), res.Error)
	}
	if res.Size != int64(sb.Len()) {
		t.Fatalf("size = %d, want %d", res.Size, sb.Len())
	}

	// The size request is clamped to the protocol cap.
	res, err = hub.hub.SendTranscriptTail(ctx, e2eWorkerID, wsproto.TranscriptTail{ReqID: "r2", Path: path, MaxBytes: 1 << 40})
	if err != nil || !res.OK || len(res.Data) != sb.Len() {
		t.Fatalf("clamped tail = %+v err=%v", res.OK, err)
	}

	outside := filepath.Join(t.TempDir(), "other.jsonl")
	_ = os.WriteFile(outside, []byte("{}\n"), 0o600)
	txt := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(txt, []byte("x"), 0o600)
	for name, p := range map[string]string{
		"outside the roots": outside, "not jsonl": txt, "relative": "s1.jsonl",
		"missing": filepath.Join(dir, "nope.jsonl"), "directory": dir,
	} {
		res, err := hub.hub.SendTranscriptTail(ctx, e2eWorkerID, wsproto.TranscriptTail{ReqID: "bad-" + strings.ReplaceAll(name, " ", "-"), Path: p})
		if err != nil {
			t.Fatalf("%s: transport error %v", name, err)
		}
		if res.OK || res.Error == "" || len(res.Data) != 0 {
			t.Fatalf("%s must be refused by the worker, got %+v", name, res)
		}
	}

	// A hub request with no req id never leaves the hub.
	if _, err := hub.hub.SendTranscriptTail(ctx, e2eWorkerID, wsproto.TranscriptTail{Path: path}); err == nil {
		t.Fatal("empty req id must be rejected")
	}
	if _, err := hub.hub.SendTranscriptTail(ctx, "nobody", wsproto.TranscriptTail{ReqID: "x", Path: path}); err == nil {
		t.Fatal("offline worker must error")
	}
}
