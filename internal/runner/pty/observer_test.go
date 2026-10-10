//go:build unix

package ptyrunner

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// fakeObserver is a SessionObserver that takes SOLE-reader ownership of the
// session and collects its output. OnSessionStart stays non-blocking (it only
// records the handoff and starts its own reader goroutine), matching the
// interface contract.
type fakeObserver struct {
	mu       sync.Mutex
	gotJobID string
	gotSess  *PtySession
	buf      bytes.Buffer

	wantN  int
	enough chan struct{} // closed once >= wantN bytes have been read
	once   sync.Once
}

func (o *fakeObserver) OnSessionStart(jobID string, sess *PtySession) {
	o.mu.Lock()
	o.gotJobID, o.gotSess = jobID, sess
	o.mu.Unlock()
	// Non-blocking: the observer reads on its OWN goroutine (Run must not block).
	go func() {
		p := make([]byte, 4096)
		for {
			n, err := sess.Read(p)
			if n > 0 {
				o.mu.Lock()
				o.buf.Write(p[:n])
				reached := o.buf.Len() >= o.wantN
				o.mu.Unlock()
				if reached {
					o.once.Do(func() { close(o.enough) })
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

// TestRunnerObserverOwnsOutput (D-P2-3, 单 reader 证明): with an observer set,
// PtyRunner.Run hands the session to it (jobID + sess) and does NOT start the
// discard drain, so the observer reads EVERY output byte with zero loss.
func TestRunnerObserverOwnsOutput(t *testing.T) {
	if !Available() {
		t.Skip("pty backend not available")
	}
	r := New()
	obs := &fakeObserver{wantN: 8, enough: make(chan struct{})}
	r.SetObserver(obs)

	// printf writes exactly 8 bytes (no newline → no ONLCR translation), then the
	// child lingers so the fd is not torn down before the observer drains it.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(ctx, runner.Request{
			JobID:   "obs1",
			Command: "sh",
			Args:    []string{"-c", "printf ABCDEFGH; sleep 30"},
		})
	}()

	select {
	case <-obs.enough:
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not receive full output (byte loss / not sole reader)")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.gotJobID != "obs1" || obs.gotSess == nil {
		t.Fatalf("observer handoff = (%q, %v), want (obs1, non-nil)", obs.gotJobID, obs.gotSess)
	}
	if got := obs.buf.String(); got != "ABCDEFGH" {
		t.Fatalf("observer output = %q, want ABCDEFGH (sole reader, zero loss)", got)
	}
}

// TestRunnerNoObserverKeepsDiscard: with no observer set the default discard
// drain is preserved, so a chatty child that nobody reads still completes (the
// slave side never wedges on a full buffer) — the G023 zero-change path.
func TestRunnerNoObserverKeepsDiscard(t *testing.T) {
	if !Available() {
		t.Skip("pty backend not available")
	}
	r := New() // no SetObserver → observer nil → discard drain kept
	res := r.Run(context.Background(), runner.Request{
		JobID:   "nodrain",
		Command: "sh",
		Args:    []string{"-c", "for i in $(seq 1 2000); do echo line$i; done"},
	})
	if res.Err != nil {
		t.Fatalf("Run err = %v, want nil (discard must drain output)", res.Err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("Run exit = %d, want 0 (discard must drain output)", res.ExitCode)
	}
}

// lateObserver is an observer whose reader is "late": it holds its first Read until
// the session's teardown has started (the child already exited and was reaped). It
// is the macOS CI timing made deterministic — there the child printed and exited
// before the observer drained a single byte (gofer-auat) — so the test asserts on
// the ORDER the product must survive, not on a scheduler accident.
type lateObserver struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	gate chan struct{} // closed when teardown reaches close-master
	done chan struct{} // closed when the reader returned
}

func newLateObserver() *lateObserver {
	return &lateObserver{gate: make(chan struct{}), done: make(chan struct{})}
}

func (o *lateObserver) OnSessionStart(_ string, sess *PtySession) {
	var once sync.Once
	sess.onEvent = func(ev string) {
		if ev == stepCloseMaster {
			once.Do(func() { close(o.gate) })
		}
	}
	go func() {
		defer close(o.done)
		<-o.gate
		p := make([]byte, 4096)
		for {
			n, err := sess.Read(p)
			o.mu.Lock()
			o.buf.Write(p[:n])
			o.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
}

func (o *lateObserver) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// TestRunnerNaturalExitKeepsUnreadOutput (gofer-auat): a child that prints and exits
// before the observer has read anything must still deliver its whole output. The
// teardown used to close the master the moment the child was reaped, so whatever
// was still in the pty buffer was thrown away (macOS lost the entire output of a
// short-lived job; a slow observer anywhere loses the tail).
func TestRunnerNaturalExitKeepsUnreadOutput(t *testing.T) {
	if !Available() {
		t.Skip("pty backend not available")
	}
	bin := testcmd.Path(t)
	r := New()
	obs := newLateObserver()
	r.SetObserver(obs)
	res := r.Run(context.Background(), runner.Request{
		JobID:   "late-reader",
		Command: bin,
		Args:    []string{"stdout-lines", "row-", "40"},
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = (exit %d, err %v), want a clean exit", res.ExitCode, res.Err)
	}
	wait.Until(t, 10*time.Second, "observer reader returns", func() bool {
		select {
		case <-obs.done:
			return true
		default:
			return false
		}
	})
	out := obs.String()
	for i := 1; i <= 40; i++ {
		if line := fmt.Sprintf("row-%d\r\n", i); !strings.Contains(out, line) {
			t.Fatalf("output lost line %q after the child exited\n%q", line, out)
		}
	}
}

// TestRunnerNaturalExitDeliversLargeOutput: a child that writes far more than one pty
// buffer and exits — every byte reaches the observer, and Run does not return before
// the reader has drained the stream (the observer only has to finish its last write).
func TestRunnerNaturalExitDeliversLargeOutput(t *testing.T) {
	if !Available() {
		t.Skip("pty backend not available")
	}
	const size = 1 << 20
	bin := testcmd.Path(t)
	r := New()
	obs := &fakeObserver{wantN: size, enough: make(chan struct{})}
	r.SetObserver(obs)
	res := r.Run(context.Background(), runner.Request{
		JobID:   "large-out",
		Command: bin,
		Args:    []string{"stdout-bytes", "x", strconv.Itoa(size)},
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = (exit %d, err %v), want a clean exit", res.ExitCode, res.Err)
	}
	wait.For(t, 10*time.Second, "observer receives every byte", func() (bool, any) {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return obs.buf.Len() == size, obs.buf.Len()
	})
}
