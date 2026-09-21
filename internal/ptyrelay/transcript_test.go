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
