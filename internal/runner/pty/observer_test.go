//go:build unix

package ptyrunner

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
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
