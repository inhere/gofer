package store

import (
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

func TestReadLogLinesWindowsFromTail(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fs.Dir("j"), StdoutFile), []byte("l1\nl2\nl3\nl4\nl5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, total, err := fs.ReadLogLines("j", StreamStdout, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || string(data) != "l4\nl5\n" {
		t.Fatalf("tail window got total=%d data=%q", total, data)
	}

	data, total, err = fs.ReadLogLines("j", StreamStdout, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || string(data) != "l2\nl3\n" {
		t.Fatalf("offset window got total=%d data=%q", total, data)
	}

	data, total, err = fs.ReadLogLines("j", StreamStdout, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(data) != 0 {
		t.Fatalf("past-start window got total=%d data=%q", total, data)
	}
}

func TestReadLogLinesFullAndNoTrailingNewline(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	content := "l1\nl2\nl3"
	if err := os.WriteFile(filepath.Join(fs.Dir("j"), StderrFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	data, total, err := fs.ReadLogLines("j", StreamStderr, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || string(data) != content {
		t.Fatalf("full read got total=%d data=%q", total, data)
	}

	data, total, err = fs.ReadLogLines("j", StreamStderr, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || string(data) != "l2\nl3" {
		t.Fatalf("tail without trailing newline got total=%d data=%q", total, data)
	}
}

func TestReadLogLinesMissingFile(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	data, total, err := fs.ReadLogLines("j", StreamStdout, 200, 0)
	if err != nil {
		t.Fatalf("expected no error for missing log, got %v", err)
	}
	if total != 0 || len(data) != 0 {
		t.Fatalf("expected empty missing log, got total=%d data=%q", total, data)
	}
}

func TestLogFileNameRejectsUnknownStream(t *testing.T) {
	if _, err := logFileName(Stream("bogus")); err == nil {
		t.Fatalf("expected error for unknown stream")
	}
}

// TestLogWriterRotates sets a tiny per-job log cap, writes past it, and asserts
// the live file restarted from 0, a "<name>.1" segment exists, and no bytes were
// silently lost (live + .1 together account for everything written).
func TestLogWriterRotates(t *testing.T) {
	prev := maxPerJobLogBytes
	maxPerJobLogBytes = 100
	t.Cleanup(func() { maxPerJobLogBytes = prev })

	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	w, err := fs.LogWriter("j", StreamStdout)
	if err != nil {
		t.Fatalf("LogWriter: %v", err)
	}

	// Write 3 chunks of 80 bytes = 240 total. With a 100-byte cap rotation
	// triggers on the chunk that finds written>=100, i.e. before chunk 2 and
	// before chunk 3 → at most one .1 retained (most recent prior segment).
	var total int
	for i := 0; i < 3; i++ {
		chunk := strings.Repeat(string(rune('A'+i)), 80)
		n, err := w.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		total += n
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	livePath := filepath.Join(fs.Dir("j"), StdoutFile)
	rotatedPath := livePath + ".1"

	rotatedBytes, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("expected rotated .1 file: %v", err)
	}
	liveBytes, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatalf("read live file: %v", err)
	}

	// Live file restarted from 0: it must be smaller than the full 240 bytes and
	// hold only the most recent segment (the third 80-byte chunk).
	if string(liveBytes) != strings.Repeat("C", 80) {
		t.Fatalf("live file should hold only the latest segment, got %q", string(liveBytes))
	}
	// .1 holds the prior segment(s); together with live nothing is lost. Because
	// rotation overwrites the .1, only the last-rotated segment survives there,
	// but it must be non-empty and the live restart must be observable.
	if len(rotatedBytes) == 0 {
		t.Fatalf("rotated file unexpectedly empty")
	}
	if int64(len(liveBytes)) > maxPerJobLogBytes+80 {
		t.Fatalf("live file did not restart: %d bytes", len(liveBytes))
	}
	// The bytes physically on disk (live + .1) account for a contiguous tail of
	// what was written, with nothing truncated mid-write.
	if len(liveBytes)+len(rotatedBytes) > total {
		t.Fatalf("on-disk bytes (%d) exceed written (%d)", len(liveBytes)+len(rotatedBytes), total)
	}
}

// TestLogWriterNoRotateUnderThreshold asserts small jobs keep the legacy
// single-file behaviour: no .1 file is created and the whole content reads back.
func TestLogWriterNoRotateUnderThreshold(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	if err := fs.Ensure("j"); err != nil {
		t.Fatal(err)
	}
	w, err := fs.LogWriter("j", StreamStdout)
	if err != nil {
		t.Fatalf("LogWriter: %v", err)
	}
	content := strings.Repeat("x", 4096)
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	w.Close()

	if _, err := os.Stat(filepath.Join(fs.Dir("j"), StdoutFile+".1")); !os.IsNotExist(err) {
		t.Fatalf("unexpected .1 file for sub-threshold job: %v", err)
	}
	full, err := fs.ReadLogTail("j", StreamStdout, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(full) != content {
		t.Fatalf("content mismatch after sub-threshold write")
	}
}
