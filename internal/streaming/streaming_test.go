package streaming

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestTailLinesOffset(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	lines := write("lines.log", "a\nb\nc\nd\n")
	for _, tc := range []struct {
		n    int
		want string
	}{{1, "d\n"}, {2, "c\nd\n"}, {4, "a\nb\nc\nd\n"}, {10, "a\nb\nc\nd\n"}, {0, "a\nb\nc\nd\n"}} {
		chunk, _, _ := TailFrom(lines, TailLinesOffset(lines, tc.n))
		if string(chunk) != tc.want {
			t.Fatalf("tail %d = %q, want %q", tc.n, chunk, tc.want)
		}
	}
	noEOL := write("noeol.log", "a\nb\nc")
	if chunk, _, _ := TailFrom(noEOL, TailLinesOffset(noEOL, 2)); string(chunk) != "b\nc" {
		t.Fatalf("no trailing newline tail = %q", chunk)
	}
	if off := TailLinesOffset(filepath.Join(dir, "missing.log"), 5); off != 0 {
		t.Fatalf("missing file offset = %d", off)
	}
	// One huge line bigger than the scan window: start inside the window, never 0.
	huge := write("huge.log", strings.Repeat("x", tailScanLimit*2)+"\nlast\n")
	chunk, _, _ := TailFrom(huge, TailLinesOffset(huge, 200))
	if len(chunk) > tailScanLimit || !strings.HasSuffix(string(chunk), "last\n") {
		t.Fatalf("huge tail len=%d suffix ok=%v", len(chunk), strings.HasSuffix(string(chunk), "last\n"))
	}
}

// TestTailChunkBoundedReplay checks the chunked replay: a file bigger than the
// limit comes back in <=limit pieces that concatenate to the original, and a
// multi-byte rune is never cut across two pieces.
func TestTailChunkBoundedReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.StdoutFile)
	body := strings.Repeat("日志行\n", 500) // 3-byte runes, boundary-sensitive
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	const limit = 100
	var got strings.Builder
	var off int64
	pieces := 0
	for {
		chunk, next, rotated := TailChunk(path, off, limit)
		if rotated {
			t.Fatal("unexpected rotation")
		}
		if len(chunk) == 0 {
			break
		}
		if len(chunk) > limit {
			t.Fatalf("piece %d has %d bytes > %d", pieces, len(chunk), limit)
		}
		if !utf8.Valid(chunk) {
			t.Fatalf("piece %d splits a rune", pieces)
		}
		got.Write(chunk)
		off = next
		pieces++
	}
	if got.String() != body || pieces < len(body)/limit {
		t.Fatalf("reassembled mismatch or too few pieces (%d)", pieces)
	}
}
