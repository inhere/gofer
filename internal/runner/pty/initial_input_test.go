package ptyrunner

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// ptyCapture is a test SessionObserver: it takes over as the session's SOLE
// reader (exactly like the serve relay and the worker pump do) and keeps
// everything the child produced, so a test can assert WHAT reached the terminal
// and in which order.
type ptyCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *ptyCapture) OnSessionStart(_ string, sess *PtySession) {
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := sess.Read(b)
			if n > 0 {
				o.mu.Lock()
				o.buf.Write(b[:n])
				o.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
}

func (o *ptyCapture) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// waitFor waits until the child's output contains substr and returns the whole
// capture (so the caller can assert ORDER, not just presence).
func (o *ptyCapture) waitFor(t *testing.T, substr string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		out := o.String()
		if strings.Contains(out, substr) {
			return out
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("output %q not seen within %s; got %q", substr, d, o.String())
	return ""
}

// startPtyJob runs one interactive job in the background and returns its result
// channel plus a cancel func.
func startPtyJob(t *testing.T, r *PtyRunner, req runner.Request) (<-chan runner.Result, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() { done <- r.Run(ctx, req) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("pty job %s did not finish after cancel", req.JobID)
		}
	})
	return done, cancel
}

// TestInitialInputWrittenAfterQuiet pins the path-B priming rule (design §9.1 B):
// the text is written to the child ONLY after it produced output and then went
// quiet — never into a terminal that has not drawn anything yet — and exactly
// once.
func TestInitialInputWrittenAfterQuiet(t *testing.T) {
	if !Available() {
		t.Skip("pty backend not available")
	}
	r := New()
	cap := &ptyCapture{}
	r.SetObserver(cap)

	startPtyJob(t, r, runner.Request{
		JobID:   "initial-quiet",
		Command: testcmd.Path(t),
		Args:    []string{"pty-echo", "BANNER"},
		// The child prints BANNER and then goes silent: the quiet window opens.
		Interactive:         true,
		InitialInput:        "hello gofer\r",
		InitialInputQuietMs: 50,
	})

	out := cap.waitFor(t, "ECHO:hello gofer", 20*time.Second)
	banner := strings.Index(out, "BANNER")
	echo := strings.Index(out, "ECHO:hello gofer")
	if banner < 0 || banner > echo {
		t.Fatalf("the input must arrive AFTER the terminal's first output (banner@%d, echo@%d): %q", banner, echo, out)
	}
	if n := strings.Count(out, "ECHO:hello gofer"); n != 1 {
		t.Fatalf("the input must be written exactly once, got %d times: %q", n, out)
	}
}

// TestInitialInputWrittenOnQuietTimeout pins the bounded side of the same rule: a
// terminal that NEVER goes quiet (a child that keeps repainting) still gets the
// text once the wait budget runs out — an unbounded wait would mean a takeover
// whose first message is never delivered.
func TestInitialInputWrittenOnQuietTimeout(t *testing.T) {
	if !Available() {
		t.Skip("pty backend not available")
	}
	r := New()
	// The production budget is 10s; the test shrinks it (and the quiet window) so
	// the case is provable in milliseconds.
	r.SetInitialInputMaxWait(400 * time.Millisecond)
	cap := &ptyCapture{}
	r.SetObserver(cap)

	startPtyJob(t, r, runner.Request{
		JobID:   "initial-timeout",
		Command: testcmd.Path(t),
		Args:    []string{"pty-echo", "BANNER", "20ms"}, // ticks every 20ms → never quiet for 50ms
		// Never quiet long enough for the 50ms window, so ONLY the max wait can
		// deliver the text.
		Interactive:         true,
		InitialInput:        "late\r",
		InitialInputQuietMs: 50,
	})

	out := cap.waitFor(t, "ECHO:late", 20*time.Second)
	echo := strings.Index(out, "ECHO:late")
	// Proof that the child was STILL producing output when the text was written:
	// the ticks it emitted before the echo outnumber what a quiet terminal would
	// ever produce (0).
	if ticks := strings.Count(out[:echo], "TICK:"); ticks < 5 {
		t.Fatalf("the text must be written while the terminal is still busy (%d ticks before the echo): %q", ticks, out)
	}
	if n := strings.Count(out, "ECHO:late"); n != 1 {
		t.Fatalf("the input must be written exactly once, got %d times: %q", n, out)
	}
}
