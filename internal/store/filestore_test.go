package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCreatesDirAndDetectsCollision(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("job1"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if fi, err := os.Stat(fs.Dir("job1")); err != nil || !fi.IsDir() {
		t.Fatalf("job dir not created: err=%v", err)
	}
	// A second Ensure of the same id must report a collision (os.ErrExist).
	// The service uses errors.Is (which unwraps); match that contract here.
	if err := fs.Ensure("job1"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected ErrExist on duplicate Ensure, got %v", err)
	}
}

func TestWriteRequestAndResultRoundTrip(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	type payload struct {
		A string `json:"a"`
		B int    `json:"b"`
	}
	if err := fs.WriteRequest("j", payload{A: "x", B: 1}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fs.Dir("j"), RequestFile)); err != nil {
		t.Fatalf("request.json missing: %v", err)
	}

	if err := fs.WriteResult("j", payload{A: "y", B: 2}); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}
	var got payload
	if err := fs.ReadResult("j", &got); err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	if got.A != "y" || got.B != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestWriteResultAtomic(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteResult("j", map[string]any{"status": "done", "exit_code": 0}); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}
	// No leftover temp file and the final file is complete, parseable JSON.
	if _, err := os.Stat(filepath.Join(fs.Dir("j"), ResultFile+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("temp result file leaked: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(fs.Dir("j"), ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("result.json is not valid JSON: %v", err)
	}
}

func TestLogWriterAndReadTail(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	w, err := fs.LogWriter("j", StreamStdout)
	if err != nil {
		t.Fatalf("LogWriter: %v", err)
	}
	content := strings.Repeat("A", 100) + strings.Repeat("B", 100)
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	w.Close()

	full, err := fs.ReadLogTail("j", StreamStdout, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(full) != content {
		t.Fatalf("full read mismatch")
	}
	tail, err := fs.ReadLogTail("j", StreamStdout, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 50 || string(tail) != strings.Repeat("B", 50) {
		t.Fatalf("tail mismatch: len=%d", len(tail))
	}
}

func TestReadLogTailMissingFile(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	// stderr never written -> empty, no error.
	b, err := fs.ReadLogTail("j", StreamStderr, 0)
	if err != nil {
		t.Fatalf("expected no error for missing log, got %v", err)
	}
	if len(b) != 0 {
		t.Fatalf("expected empty tail, got %d bytes", len(b))
	}
}

func TestAppendIndexAndReadIndex(t *testing.T) {
	base := t.TempDir()
	fs := NewFileStore(base)
	if err := fs.AppendIndex(map[string]any{"id": "a", "status": "queued"}); err != nil {
		t.Fatalf("AppendIndex 1: %v", err)
	}
	if err := fs.AppendIndex(map[string]any{"id": "a", "status": "done"}); err != nil {
		t.Fatalf("AppendIndex 2: %v", err)
	}
	recs, err := fs.ReadIndex()
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 index lines, got %d", len(recs))
	}
	var last map[string]any
	if err := json.Unmarshal(recs[1], &last); err != nil {
		t.Fatalf("last line not valid JSON: %v", err)
	}
	if last["status"] != "done" {
		t.Fatalf("expected last status=done, got %v", last["status"])
	}
}

func TestReadIndexMissingFile(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	recs, err := fs.ReadIndex()
	if err != nil {
		t.Fatalf("expected no error for missing index, got %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("expected empty slice, got %d", len(recs))
	}
}

func TestReadIndexSkipsCorruptLines(t *testing.T) {
	base := t.TempDir()
	fs := NewFileStore(base)
	if err := fs.AppendIndex(map[string]any{"id": "ok1"}); err != nil {
		t.Fatal(err)
	}
	// Append a raw garbage line directly to the index file.
	f, err := os.OpenFile(filepath.Join(base, IndexFile), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	recs, err := fs.ReadIndex()
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 valid line (corrupt skipped), got %d", len(recs))
	}
	var m map[string]any
	if err := json.Unmarshal(recs[0], &m); err != nil {
		t.Fatalf("surviving line not valid JSON: %v", err)
	}
	if m["id"] != "ok1" {
		t.Fatalf("expected id=ok1, got %v", m["id"])
	}
}

func TestInteractionsFileConstantReserved(t *testing.T) {
	// P4 only reserves the name; nothing should create it.
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	if InteractionsFile != "interactions.jsonl" {
		t.Fatalf("unexpected InteractionsFile name: %q", InteractionsFile)
	}
	if _, err := os.Stat(filepath.Join(fs.Dir("j"), InteractionsFile)); !os.IsNotExist(err) {
		t.Fatalf("interactions.jsonl should not exist in P4")
	}
}
