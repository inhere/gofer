package ptyrelay

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// fakePtySource is a scriptable PtySource for the relay tests: output is fed via
// Emit() (delivered to the recorder on Read), and input/resize are captured for
// assertion.
type fakePtySource struct {
	outCh chan []byte

	mu       sync.Mutex
	writes   [][]byte
	resizes  [][2]int
	closed   bool
	leftover []byte
}

func newFakeSource() *fakePtySource {
	return &fakePtySource{outCh: make(chan []byte, 1024)}
}

// Emit queues a chunk of output for the recorder to Read.
func (f *fakePtySource) Emit(b []byte) { f.outCh <- b }

// EmitDone signals EOF after queued output drains.
func (f *fakePtySource) EmitDone() { close(f.outCh) }

func (f *fakePtySource) Read(p []byte) (int, error) {
	if len(f.leftover) > 0 {
		n := copy(p, f.leftover)
		f.leftover = f.leftover[n:]
		return n, nil
	}
	chunk, ok := <-f.outCh
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, chunk)
	if n < len(chunk) {
		f.leftover = append([]byte(nil), chunk[n:]...)
	}
	return n, nil
}

func (f *fakePtySource) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (f *fakePtySource) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakePtySource) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakePtySource) Writes() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
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

// TestPreAttachScrollback (证明点3-A): output produced BEFORE any viewer attaches
// lands in the ring; the first viewer then replays the ring tail (pre-attach
// scrollback, K6).
func TestPreAttachScrollback(t *testing.T) {
	src := newFakeSource()
	r := New(src, WithRingSize(1024))
	r.Start()

	pre := []byte("PRE-ATTACH-OUTPUT-1234567890")
	src.Emit(pre)
	// Deterministically wait until the recorder has taken the pre-attach bytes.
	waitFor(t, 2*time.Second, func() bool { return r.RecordedLen() >= len(pre) })

	// First viewer attaches AFTER the output was produced.
	v, err := r.AddViewer(true)
	if err != nil {
		t.Fatalf("AddViewer: %v", err)
	}
	defer v.Close()

	scroll := r.Scrollback()
	if !bytes.Contains(scroll, pre) {
		t.Fatalf("scrollback missing pre-attach output: got %q", scroll)
	}

	// Post-attach output flows live to the viewer's queue.
	post := []byte("LIVE-AFTER-ATTACH")
	src.Emit(post)
	got := readViewer(t, v, len(post), time.Second)
	if !bytes.Contains(got, post) {
		t.Fatalf("viewer missing live output: got %q", got)
	}
}

// TestSlowViewerDoesNotBlock (证明点3-B): a slow viewer that never drains its
// queue does NOT stall the recorder or a fast viewer — proving the two
// backpressure layers are independent (K2). The recorder must consume all
// source output and the fast viewer must receive all of it.
func TestSlowViewerDoesNotBlock(t *testing.T) {
	src := newFakeSource()
	r := New(src, WithRingSize(1<<20))
	r.Start()

	const chunks = 200
	// Fast viewer: queue deep enough that a draining consumer never overflows.
	fast, err := r.AddViewerWithQueue(true, chunks+10)
	if err != nil {
		t.Fatalf("AddViewer fast: %v", err)
	}
	// Slow viewer: shallow queue, never drained → overflows and lags, WITHOUT
	// stalling the recorder or the fast viewer.
	slow, err := r.AddViewerWithQueue(false, 2)
	if err != nil {
		t.Fatalf("AddViewer slow: %v", err)
	}
	_ = slow // intentionally never read from
	// Fast viewer drains everything concurrently.
	var got int
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for range fast.Out() {
			mu.Lock()
			got++
			mu.Unlock()
		}
		close(done)
	}()

	total := 0
	for i := 0; i < chunks; i++ {
		b := []byte(fmt.Sprintf("chunk-%03d\n", i))
		total += len(b)
		src.Emit(b) // must not block despite the stalled slow viewer
	}
	// Recorder must have consumed EVERY byte (main path never gated on viewers).
	waitFor(t, 3*time.Second, func() bool { return r.RecordedLen() >= total })

	src.EmitDone()
	<-r.Done() // recorder saw EOF and closed → fast.Out() closes → drain goroutine ends
	<-done

	mu.Lock()
	fastGot := got
	mu.Unlock()
	if fastGot != chunks {
		t.Fatalf("fast viewer should receive all %d chunks, got %d", chunks, fastGot)
	}
	if fast.Lagged() {
		t.Fatalf("fast (draining) viewer must not be marked lagged")
	}
	if !slow.Lagged() {
		t.Fatalf("slow viewer expected to be marked lagged (dropped refreshes)")
	}
}

// TestInputLeaseExclusive (证明点3-C): the first writer owns the exclusive input
// lease; a second writer is refused; a read-only follower cannot write; the
// lease holder's input reaches the source (K3).
func TestInputLeaseExclusive(t *testing.T) {
	src := newFakeSource()
	r := New(src)
	r.Start()

	w1, err := r.AddViewer(true)
	if err != nil {
		t.Fatalf("first writer AddViewer: %v", err)
	}
	// Second writer refused while the lease is held.
	if _, err := r.AddViewer(true); err != ErrLeaseTaken {
		t.Fatalf("second writer should be refused with ErrLeaseTaken, got %v", err)
	}
	// A read-only follower is admitted but cannot write.
	ro, err := r.AddViewer(false)
	if err != nil {
		t.Fatalf("read-only AddViewer: %v", err)
	}
	if err := ro.SendInput([]byte("x")); err != ErrReadOnly {
		t.Fatalf("read-only viewer write should be ErrReadOnly, got %v", err)
	}

	// The lease holder's input reaches the source.
	if err := w1.SendInput([]byte("ls\n")); err != nil {
		t.Fatalf("lease holder SendInput: %v", err)
	}
	waitFor(t, time.Second, func() bool { return len(src.Writes()) == 1 })
	if got := string(src.Writes()[0]); got != "ls\n" {
		t.Fatalf("source got wrong input: %q", got)
	}

	// Releasing the lease lets a new writer take it (attach/detach cycle).
	w1.Close()
	w2, err := r.AddViewer(true)
	if err != nil {
		t.Fatalf("re-acquire lease after release: %v", err)
	}
	if err := w2.SendInput([]byte("pwd\n")); err != nil {
		t.Fatalf("new lease holder SendInput: %v", err)
	}
}

// readViewer reads from a viewer's output queue until it has accumulated at least
// want bytes or the deadline elapses.
func readViewer(t *testing.T, v *Viewer, want int, d time.Duration) []byte {
	t.Helper()
	var buf bytes.Buffer
	timer := time.NewTimer(d)
	defer timer.Stop()
	for buf.Len() < want {
		select {
		case b, ok := <-v.Out():
			if !ok {
				return buf.Bytes()
			}
			buf.Write(b)
		case <-timer.C:
			return buf.Bytes()
		}
	}
	return buf.Bytes()
}
