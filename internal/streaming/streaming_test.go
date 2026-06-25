package streaming

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/store"
)

// TestTailFromDetectsRotation unit-tests the rotation signal: once the live file
// shrinks below the caller's offset, TailFrom reports rotated=true (empty chunk),
// and a subsequent read from offset 0 returns the fresh content. This is the
// exact protocol the SSE loop uses to emit a `log-rotated` marker and reset.
func TestTailFromDetectsRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, store.StdoutFile)

	if err := os.WriteFile(path, []byte("AAAAAAAAAA"), 0o644); err != nil { // 10 bytes
		t.Fatal(err)
	}
	chunk, next, rotated := TailFrom(path, 0)
	if rotated || string(chunk) != "AAAAAAAAAA" || next != 10 {
		t.Fatalf("initial read: chunk=%q next=%d rotated=%v", chunk, next, rotated)
	}

	// Simulate a rotation: the live file is replaced by a smaller fresh file.
	if err := os.WriteFile(path, []byte("BBB"), 0o644); err != nil { // 3 bytes < offset 10
		t.Fatal(err)
	}
	// Reading from the stale offset must flag rotation and return no bytes.
	chunk, _, rotated = TailFrom(path, next)
	if !rotated {
		t.Fatalf("expected rotated=true when file shrank below offset")
	}
	if len(chunk) != 0 {
		t.Fatalf("rotated read must return empty chunk, got %q", chunk)
	}
	// Re-reading from 0 yields the fresh file's content with no bleed of the old tail.
	chunk, next, rotated = TailFrom(path, 0)
	if rotated || string(chunk) != "BBB" || next != 3 {
		t.Fatalf("post-rotation read: chunk=%q next=%d rotated=%v", chunk, next, rotated)
	}
}
