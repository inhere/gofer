//go:build unix

package pty

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestStartSizeAndExit proves the whole unix contract in one shot: Start applies
// the initial window size (a size-sensitive program reads it), Read carries the
// child's output, and Wait returns a clean exit code.
func TestStartSizeAndExit(t *testing.T) {
	if !IsAvailable() {
		t.Skip("pty backend not available")
	}
	// `stty size` prints "<rows> <cols>"; we start it at 40x120.
	p, err := Start(Spec{
		Command: "sh",
		Args:    []string{"-c", "stty size; echo done"},
		Cols:    120,
		Rows:    40,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	out := readAll(t, p, 2*time.Second)
	if !strings.Contains(out, "40 120") {
		t.Fatalf("stty size did not reflect initial 40x120, got %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("missing child output, got %q", out)
	}

	code, werr := p.Wait(context.Background())
	if werr != nil {
		t.Fatalf("Wait err: %v", werr)
	}
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

// TestResizeAndIO proves Resize succeeds on a live pty and that input written to
// the master is echoed back on Read (a real interactive round-trip through cat).
func TestResizeAndIO(t *testing.T) {
	if !IsAvailable() {
		t.Skip("pty backend not available")
	}
	p, err := Start(Spec{Command: "cat", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	if err := p.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if _, err := p.Write([]byte("hello-pty\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// cat echoes the line back through the pty (terminal echo + program echo).
	got := readUntil(t, p, "hello-pty", 2*time.Second)
	if !strings.Contains(got, "hello-pty") {
		t.Fatalf("input not echoed, got %q", got)
	}
}

// readAll reads to EOF (bounded by a deadline via a watchdog Close).
func readAll(t *testing.T, r io.ReadCloser, d time.Duration) string {
	t.Helper()
	done := make(chan struct{})
	timer := time.AfterFunc(d, func() { _ = r.Close() })
	defer timer.Stop()
	var sb strings.Builder
	go func() {
		_, _ = io.Copy(&sb, r)
		close(done)
	}()
	<-done
	return sb.String()
}

// readUntil reads until the accumulated output contains needle or the deadline
// elapses.
func readUntil(t *testing.T, r io.Reader, needle string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	var sb strings.Builder
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		type rr struct {
			n   int
			err error
		}
		ch := make(chan rr, 1)
		go func() { n, err := r.Read(buf); ch <- rr{n, err} }()
		select {
		case res := <-ch:
			if res.n > 0 {
				sb.Write(buf[:res.n])
				if strings.Contains(sb.String(), needle) {
					return sb.String()
				}
			}
			if res.err != nil {
				return sb.String()
			}
		case <-time.After(time.Until(deadline)):
			return sb.String()
		}
	}
	return sb.String()
}
