package store

import (
	"errors"
	"os"
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

func TestLogFileNameRejectsUnknownStream(t *testing.T) {
	if _, err := logFileName(Stream("bogus")); err == nil {
		t.Fatalf("expected error for unknown stream")
	}
}
