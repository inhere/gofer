//go:build unix

package ptyrunner

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/pty"
	"github.com/inhere/gofer/internal/runner"
)

// recorder captures session events (state transitions + teardown steps) in order.
type recorder struct {
	mu sync.Mutex
	ev []string
}

func (r *recorder) hook(ev string) {
	r.mu.Lock()
	r.ev = append(r.ev, ev)
	r.mu.Unlock()
}
func (r *recorder) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ev...)
}

// TestCancelOrderedTeardown (证明点2): cancelling a running interactive session
// goes cancelling → the FIXED close sequence (stop-input → close-master →
// kill-child → wait-child → publish-exit) → closed, and Run returns ONLY after
// the child is reaped and the exit published. This is the deliberate contrast
// with local.Runner, whose exec.CommandContext cancel returns as soon as Wait
// unblocks with NO ordered teardown / publish step.
func TestCancelOrderedTeardown(t *testing.T) {
	if !pty.IsAvailable() {
		t.Skip("pty backend not available")
	}
	// `cat` blocks reading stdin → a real interactive child that only ends on
	// kill (proving teardown drives the exit, not the child on its own).
	p, err := pty.Start(pty.Spec{Command: "cat", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	sess := newSession("job-cancel", p)
	rec := &recorder{}
	sess.onEvent = rec.hook
	// Drain output so the slave side never wedges (mirrors PtyRunner.Run).
	go func() { _, _ = io.Copy(io.Discard, sess) }()

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		code int
		err  error
	}
	runDone := make(chan result, 1)
	go func() {
		code, rerr := sess.run(ctx)
		runDone <- result{code, rerr}
	}()

	waitForState(t, sess, StateRunning, 2*time.Second)

	// Cancel and time how long Run blocks: it must NOT return before teardown.
	cancel()
	var res result
	select {
	case res = <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel within grace")
	}

	if res.err != context.Canceled {
		t.Fatalf("expected ctx.Canceled from cancelled run, got %v", res.err)
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed, got %s", sess.State())
	}
	// The child must actually have been reaped (not merely signalled): childExited
	// is closed by the single pty.Wait, and teardown blocks on it at wait-child.
	select {
	case <-sess.childExited:
	default:
		t.Fatal("child was not reaped — cancel returned without waiting (signal-and-return)")
	}

	// Assert the exact ordered protocol (state:exiting is entered at the top of
	// the close sequence, before the first step).
	want := []string{
		"state:" + StateRunning,
		"state:" + StateCancelling,
		"state:" + StateExiting,
		stepStopInput,
		stepCloseMaster,
		stepKillChild,
		stepWaitChild,
		stepPublishExit,
		"state:" + StateClosed,
	}
	assertSubsequence(t, rec.events(), want)
	// wait-child MUST precede publish-exit (proves Run waited for the reap).
	assertOrder(t, rec.events(), stepWaitChild, stepPublishExit)
}

// TestNaturalExitTeardown: a child that exits on its own still funnels through
// the SAME single teardown path (one terminal path), publishing its real exit
// code, without a kill step.
func TestNaturalExitTeardown(t *testing.T) {
	if !pty.IsAvailable() {
		t.Skip("pty backend not available")
	}
	p, err := pty.Start(pty.Spec{Command: "sh", Args: []string{"-c", "exit 7"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	sess := newSession("job-nat", p)
	rec := &recorder{}
	sess.onEvent = rec.hook
	go func() { _, _ = io.Copy(io.Discard, sess) }()

	code, rerr := sess.run(context.Background())
	if rerr != nil {
		t.Fatalf("natural exit should not error, got %v", rerr)
	}
	if code != 7 {
		t.Fatalf("expected exit code 7, got %d", code)
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed, got %s", sess.State())
	}
	ev := rec.events()
	// No cancelling / kill step on a natural exit.
	if contains(ev, "state:"+StateCancelling) {
		t.Fatalf("natural exit must not enter cancelling: %v", ev)
	}
	if contains(ev, stepKillChild) {
		t.Fatalf("natural exit must not kill: %v", ev)
	}
	assertOrder(t, ev, stepWaitChild, stepPublishExit)
}

// TestRunnerRunAndRegistry drives the full PtyRunner.Run: the session is
// registered while running and removed after, and cancel maps to a cancelled
// Result (ctx err surfaced like local.Runner).
func TestRunnerRunAndRegistry(t *testing.T) {
	if !Available() {
		t.Skip("pty backend not available")
	}
	r := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(ctx, runner.Request{JobID: "j1", Command: "cat"})
	}()

	// The session is discoverable in the registry while the job runs.
	waitFor(t, 2*time.Second, func() bool {
		_, ok := r.Sessions().Lookup("j1")
		return ok
	})
	cancel()

	select {
	case res := <-done:
		if res.Err != context.Canceled {
			t.Fatalf("expected ctx.Canceled, got %v", res.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PtyRunner.Run did not return after cancel")
	}
	// Deregistered after Run returns.
	waitFor(t, 2*time.Second, func() bool {
		_, ok := r.Sessions().Lookup("j1")
		return !ok
	})
}

// --- helpers ---

func waitForState(t *testing.T, s *PtySession, state string, d time.Duration) {
	t.Helper()
	waitFor(t, d, func() bool { return s.State() == state })
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// assertSubsequence checks want appears in got in the SAME relative order (other
// events may interleave).
func assertSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, ev := range got {
		if i < len(want) && ev == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("event subsequence not satisfied at %q\n got:  %v\n want: %v", want[i], got, want)
	}
}

// assertOrder checks a appears before b in got.
func assertOrder(t *testing.T, got []string, a, b string) {
	t.Helper()
	ia, ib := -1, -1
	for i, ev := range got {
		if ev == a && ia < 0 {
			ia = i
		}
		if ev == b && ib < 0 {
			ib = i
		}
	}
	if ia < 0 || ib < 0 || ia >= ib {
		t.Fatalf("expected %q before %q in %v", a, b, got)
	}
}
