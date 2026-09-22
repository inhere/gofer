package ptyrelay

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTranscriptStripsAnsiAndFoldsCR pins the text filter (PTY-01 §四): CSI, OSC,
// charset and two-byte escapes disappear; CRLF folds to ONE newline and a lone CR
// becomes one too. The escape sequences are SPLIT across Write calls on purpose —
// a 4KB pty read cuts them mid-sequence, and a stateless filter would leak the
// tail into the transcript.
func TestTranscriptStripsAnsiAndFoldsCR(t *testing.T) {
	var s Stripper
	var got []byte
	// "\x1b[1;32m" (CSI) + "hello" + "\x1b]0;title\x07" (OSC/BEL) split in half.
	got = append(got, s.Write([]byte("\x1b[1;3"))...)
	got = append(got, s.Write([]byte("2mhello\x1b]0;ti"))...)
	got = append(got, s.Write([]byte("tle\x07 \x1b(Bworld\x1b[0m"))...)
	// CRLF must fold to one newline, a lone CR to one, and a CR split across two
	// writes must not double up.
	got = append(got, s.Write([]byte("\r\nnext"))...)
	got = append(got, s.Write([]byte("\rtail\rtail2"))...)
	got = append(got, s.Write([]byte("\r"))...)
	got = append(got, s.Flush()...)

	want := "hello world\nnext\ntail\ntail2\n"
	if string(got) != want {
		t.Fatalf("stripped text = %q, want %q", got, want)
	}
}

// TestTranscriptStripsOSCWithSTTerminator covers the other OSC terminator (ESC \)
// and the private-mode CSI forms a TUI emits on startup/exit.
func TestTranscriptStripsOSCWithSTTerminator(t *testing.T) {
	var s Stripper
	got := s.Write([]byte("\x1b]0;win\x1b\\\x1b[?25lvisible\x1b[?25h\x1b[2J"))
	if string(got) != "visible" {
		t.Fatalf("stripped text = %q, want %q", got, "visible")
	}
}

// TestTranscriptCursorForwardBecomesSpaces is the transcript half of F6: claude's ink
// TUI pads columns with CUF (`ESC[nC`, cursor forward) instead of spaces, so dropping
// the sequence glued words together — the real pty.txt read
// `NewMCPserverfoundinthisproject:aliyun-slsMCPserversmayexecutecode…`. CUF must become
// n spaces, and the sequences arrive split across 4KB pty reads, so the parameter has to
// survive a Write boundary.
func TestTranscriptCursorForwardBecomesSpaces(t *testing.T) {
	// The real shape: `New<CUF 1>MCP<CUF 3>server` -> `New MCP   server`.
	var s Stripper
	if got := s.Write([]byte("New\x1b[1CMCP\x1b[3Cserver")); string(got) != "New MCP   server" {
		t.Fatalf("stripped = %q, want %q", got, "New MCP   server")
	}
	// Default parameter (`ESC[C` = one column) and the split-across-writes form.
	var d Stripper
	if got := d.Write([]byte("a\x1b[Cb")); string(got) != "a b" {
		t.Fatalf("default-param CUF = %q, want %q", got, "a b")
	}
	var sp Stripper
	got := append(sp.Write([]byte("a\x1b[1")), sp.Write([]byte("Cb"))...)
	if string(got) != "a b" {
		t.Fatalf("split CUF = %q, want %q", got, "a b")
	}
	// A corrupt/absurd parameter is clamped, not obeyed (the transcript must not blow up).
	var big Stripper
	if got := big.Write([]byte("a\x1b[999999Cb")); len(got) != 202 {
		t.Fatalf("clamped CUF length = %d, want 202 (clamped to 200 spaces)", len(got))
	}
}

// TestTranscriptAbsoluteMoveSeparatesWords: an absolute move (`ESC[nG` CHA, `ESC[r;cH`
// CUP) is layout, not text, so it becomes at most ONE space — just enough to keep two
// words apart without a screen model. No space is added when the text already ends in
// whitespace (or when nothing has been printed yet), and an erase (`ESC[K`/`ESC[J`)
// stays invisible.
func TestTranscriptAbsoluteMoveSeparatesWords(t *testing.T) {
	var s Stripper
	if got := s.Write([]byte("Enter\x1b[10Gto\x1b[2;5Hconfirm")); string(got) != "Enter to confirm" {
		t.Fatalf("stripped = %q, want %q", got, "Enter to confirm")
	}
	// Already separated: the column move must not double the space, and being right
	// after a newline it must not add a leading one either.
	var sp Stripper
	if got := sp.Write([]byte("Enter \x1b[10Gto\na\x1b[3Gb")); string(got) != "Enter to\na b" {
		t.Fatalf("stripped = %q, want %q", got, "Enter to\na b")
	}
	// Erase prints nothing, and a move at the very start cannot invent a leading space.
	var e Stripper
	if got := e.Write([]byte("\x1b[2K\x1b[1;1Hredraw\x1b[K")); string(got) != "redraw" {
		t.Fatalf("stripped = %q, want %q", got, "redraw")
	}
}

// TestTranscriptPrivateModeStillStripped: private-mode CSI forms (`ESC[?25l` hide
// cursor / `ESC[?25h` show) are decoration with a `?` parameter — they stay invisible
// and are never mistaken for a cursor move.
func TestTranscriptPrivateModeStillStripped(t *testing.T) {
	var s Stripper
	if got := s.Write([]byte("\x1b[?25la\x1b[?25h")); string(got) != "a" {
		t.Fatalf("stripped = %q, want %q", got, "a")
	}
	var mid Stripper
	if got := mid.Write([]byte("a\x1b[?25lb")); string(got) != "ab" {
		t.Fatalf("stripped = %q, want %q", got, "ab")
	}
}

// TestTranscriptRingKeepsTail proves the cap is a TAIL cap, in memory and on disk:
// a transcript larger than max retains only the last max bytes of text.
func TestTranscriptRingKeepsTail(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "pty.txt"
	tr := NewTranscript(path, 32)

	head := strings.Repeat("a", 100)
	tail := "KEEPME"
	if _, err := tr.Write([]byte(head + tail)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// 106 distinct bytes: the retained tail is the last 32, ending with KEEPME.
	got := tr.Tail()
	if len(got) != 32 {
		t.Fatalf("tail len = %d, want 32 (%q)", len(got), got)
	}
	if !bytes.HasSuffix(got, []byte(tail)) {
		t.Fatalf("tail %q does not end with %q", got, tail)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if len(onDisk) != 32 || !bytes.HasSuffix(onDisk, []byte(tail)) {
		t.Fatalf("pty.txt = %d bytes %q, want the 32-byte tail ending in %q", len(onDisk), onDisk, tail)
	}
}

// TestTranscriptFlushOnClose pins the file lifecycle: nothing is written until a
// flush is due (no empty pty.txt from a silent session), and Close flushes the
// remaining text.
func TestTranscriptFlushOnClose(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "pty.txt"
	tr := NewTranscript(path, 1<<20)

	if _, err := tr.Write([]byte("first line\x1b[0m\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pty.txt exists before any flush (stat err=%v)", err)
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(data) != "first line\n" {
		t.Fatalf("pty.txt = %q, want %q", data, "first line\n")
	}
}

// TestTranscriptFlushesWhileRunning proves the running-session flush: once a
// flush window's worth of text has arrived, it is ON DISK before the session
// closes (that is what lets `job logs`/the web page follow a live TUI).
func TestTranscriptFlushesWhileRunning(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "pty.txt"
	tr := NewTranscript(path, 1<<20)
	t.Cleanup(func() { _ = tr.Close() })

	chunk := strings.Repeat("x", transcriptFlushBytes)
	if _, err := tr.Write([]byte(chunk)); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Size() >= int64(len(chunk)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pty.txt was not flushed while the session was still running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRelayWritesAndClosesTranscript proves the relay's main path owns the
// transcript: every recorded chunk reaches it and finish() seals the file (a
// close hook must also see the completed tail).
func TestRelayWritesAndClosesTranscript(t *testing.T) {
	src := newFakeSource()
	path := t.TempDir() + string(os.PathSeparator) + "pty.txt"
	tr := NewTranscript(path, 1<<20)
	hooked := false

	r := New(src, WithTranscript(tr), WithCloseHook(func() { hooked = true }))
	r.Start()
	src.Emit([]byte("\x1b[32mlive\x1b[0m output\r\n"))
	waitFor(t, time.Second, func() bool { return r.RecordedLen() >= len("\x1b[32mlive\x1b[0m output\r\n") })
	src.EmitDone()

	select {
	case <-r.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not finish")
	}
	if !hooked {
		t.Fatal("close hook did not run")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(data) != "live output\n" {
		t.Fatalf("pty.txt = %q, want %q", data, "live output\n")
	}
}
