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

// fakeCast is a scriptable CastSink that records the EXACT order of Write and
// Close calls (to prove cast.Write never races cast.Close, B1) and can optionally
// block in Close until released (to exercise boundedCastClose's grace timeout).
type fakeCast struct {
	mu      sync.Mutex
	events  []string // "w" per Write, "c" per Close, in call order
	written []byte
	closed  bool

	closeEntered chan struct{} // closed once Close is entered (nil = don't signal)
	closeBlock   chan struct{} // if non-nil, Close blocks until this is closed
}

func (c *fakeCast) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.events = append(c.events, "w")
	c.written = append(c.written, p...)
	c.mu.Unlock()
	return len(p), nil
}

func (c *fakeCast) Close() error {
	c.mu.Lock()
	c.events = append(c.events, "c")
	c.closed = true
	block := c.closeBlock
	c.mu.Unlock()
	if c.closeEntered != nil {
		close(c.closeEntered) // Close runs once (finishOnce) → single close is safe
	}
	if block != nil {
		<-block
	}
	return nil
}

func (c *fakeCast) snapshot() (events []string, written []byte, closed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...), append([]byte(nil), c.written...), c.closed
}

// waitDone asserts r.Done() closes within d.
func waitDone(t *testing.T, r *Relay, d time.Duration) {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(d):
		t.Fatalf("Done() not closed within %s", d)
	}
}

// TestFinishClosesDoneMatrix: finish() ALWAYS closes done — with cast, without
// cast, and for a relay that was never Start()ed (no recordLoop to own finish).
func TestFinishClosesDoneMatrix(t *testing.T) {
	t.Run("started_no_cast_eof", func(t *testing.T) {
		src := newFakeSource()
		r := New(src)
		r.Start()
		src.EmitDone() // EOF → recordLoop exits → finish closes done
		waitDone(t, r, 2*time.Second)
	})
	t.Run("started_with_cast_eof", func(t *testing.T) {
		src := newFakeSource()
		fc := &fakeCast{}
		r := New(src, WithCast(fc))
		r.Start()
		src.EmitDone()
		waitDone(t, r, 2*time.Second)
		if _, _, closed := fc.snapshot(); !closed {
			t.Fatal("cast not closed by finish on started+cast EOF path")
		}
	})
	t.Run("never_started_no_cast", func(t *testing.T) {
		src := newFakeSource()
		r := New(src)
		if err := r.Close(); err != nil { // never Start()ed → Close owns finish
			t.Fatalf("Close: %v", err)
		}
		waitDone(t, r, 2*time.Second)
	})
	t.Run("never_started_with_cast", func(t *testing.T) {
		src := newFakeSource()
		fc := &fakeCast{}
		r := New(src, WithCast(fc))
		_ = r.Close() // Close owns finish → closes cast then done, no writes ever
		waitDone(t, r, 2*time.Second)
		ev, _, closed := fc.snapshot()
		if !closed {
			t.Fatal("cast not closed on never-started+cast Close path")
		}
		if len(ev) != 1 || ev[0] != "c" {
			t.Fatalf("never-started cast events = %v, want exactly one Close (no writes)", ev)
		}
	})
}

// TestCastClosedAfterAllWrites (B1 core): the cast sink is Closed exactly once and
// AFTER every Write — proving cast.Close never races the recordLoop's cast.Write.
func TestCastClosedAfterAllWrites(t *testing.T) {
	src := newFakeSource()
	fc := &fakeCast{}
	r := New(src, WithCast(fc))
	r.Start()

	const n = 8
	total := 0
	for i := 0; i < n; i++ {
		b := []byte(fmt.Sprintf("chunk-%02d;", i))
		total += len(b)
		src.Emit(b)
	}
	waitFor(t, 3*time.Second, func() bool { return r.RecordedLen() >= total })
	src.EmitDone()
	waitDone(t, r, 2*time.Second)

	ev, written, closed := fc.snapshot()
	if !closed {
		t.Fatal("cast not closed")
	}
	// Exactly one Close, and it is the LAST event; everything before it is a Write.
	if len(ev) == 0 || ev[len(ev)-1] != "c" {
		t.Fatalf("last event = %v, want Close last", ev)
	}
	closes := 0
	for i, e := range ev {
		if e == "c" {
			closes++
			if i != len(ev)-1 {
				t.Fatalf("Close at index %d is not last (events=%v) → Write raced Close", i, ev)
			}
		}
	}
	if closes != 1 {
		t.Fatalf("Close called %d times, want exactly 1", closes)
	}
	if len(written) != total {
		t.Fatalf("cast wrote %d bytes, want %d", len(written), total)
	}
}

// TestStartedCloseDoesNotFinish (B1): for a Start()ed relay, Close() must NOT run
// finish (that is the recordLoop's job — Close doing it would race cast.Write).
// Here the source's Read stays parked after Close, so if Close wrongly owned
// finish the cast would close early; we assert it does not until EOF drains.
func TestStartedCloseDoesNotFinish(t *testing.T) {
	src := newFakeSource()
	fc := &fakeCast{}
	r := New(src, WithCast(fc))
	r.Start()

	pre := []byte("before-close")
	src.Emit(pre)
	waitFor(t, 2*time.Second, func() bool { return r.RecordedLen() >= len(pre) })

	// Close a started relay: fakeSource.Close does NOT unblock the parked Read, so
	// the recordLoop is still alive → finish (hence cast.Close) must NOT have run.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, closed := fc.snapshot(); closed {
		t.Fatal("Close() on a started relay wrongly closed the cast (finish must belong to recordLoop)")
	}
	select {
	case <-r.Done():
		t.Fatal("Done closed by Close() on a started relay; recordLoop still owns finish")
	default:
	}

	// Now let the source hit EOF → recordLoop exits → its deferred finish封尾s cast.
	src.EmitDone()
	waitDone(t, r, 2*time.Second)
	if _, _, closed := fc.snapshot(); !closed {
		t.Fatal("cast not closed after recordLoop EOF")
	}
}

// TestBoundedCastCloseTimeout: a wedged cast.Close does NOT block Done past
// castCloseGrace — Done still fires (the host stays bounded, P2 invariant).
func TestBoundedCastCloseTimeout(t *testing.T) {
	orig := castCloseGrace
	castCloseGrace = 50 * time.Millisecond
	t.Cleanup(func() { castCloseGrace = orig })

	release := make(chan struct{})
	fc := &fakeCast{closeEntered: make(chan struct{}), closeBlock: release}
	t.Cleanup(func() { close(release) }) // release the wedged Close so no goroutine leaks

	src := newFakeSource()
	r := New(src, WithCast(fc))
	r.Start()
	src.EmitDone() // EOF → finish → boundedCastClose (cast.Close wedges)

	// Done must fire within grace + margin despite the wedged Close.
	waitDone(t, r, 1*time.Second)
	// And Close was actually entered (grace path, not skipped).
	select {
	case <-fc.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("cast.Close was never entered")
	}
}

// TestInputLenCounts (D-P3-8): InputLen sums stdin bytes forwarded by the write
// lease holder; a read-only viewer's refused input is never counted.
func TestInputLenCounts(t *testing.T) {
	src := newFakeSource()
	r := New(src)
	r.Start()
	t.Cleanup(func() { _ = r.Close() })

	w, err := r.AddViewer(true)
	if err != nil {
		t.Fatalf("AddViewer writer: %v", err)
	}
	ro, err := r.AddViewer(false)
	if err != nil {
		t.Fatalf("AddViewer read-only: %v", err)
	}
	if err := w.SendInput([]byte("abc")); err != nil {
		t.Fatalf("SendInput abc: %v", err)
	}
	if err := w.SendInput([]byte("de")); err != nil {
		t.Fatalf("SendInput de: %v", err)
	}
	if err := ro.SendInput([]byte("XXXX")); err != ErrReadOnly {
		t.Fatalf("read-only SendInput = %v, want ErrReadOnly", err)
	}
	if got := r.InputLen(); got != 5 {
		t.Fatalf("InputLen = %d, want 5 (read-only input not counted)", got)
	}
}

// TestWithCastViaOpen (M1,消死代码): WithCast wired through the Registry's variadic
// Open reaches the Relay's cast sink.
func TestWithCastViaOpen(t *testing.T) {
	reg := NewRegistry()
	reg.Prepare(RelayBinding{JobID: "j", PtySessionID: "p", Nonce: "n"})
	src := newFakeSource()
	fc := &fakeCast{}
	entry, err := reg.Open("n", src, WithCast(fc))
	if err != nil {
		t.Fatalf("Open with cast opt: %v", err)
	}
	chunk := []byte("hello-cast")
	src.Emit(chunk)
	waitFor(t, 2*time.Second, func() bool { return entry.Relay.RecordedLen() >= len(chunk) })
	if _, written, _ := fc.snapshot(); !bytes.Equal(written, chunk) {
		t.Fatalf("cast via Open(opts) got %q, want %q", written, chunk)
	}
	src.EmitDone()
	waitDone(t, entry.Relay, 2*time.Second)
	if _, _, closed := fc.snapshot(); !closed {
		t.Fatal("cast opened via Open(opts) not closed on EOF")
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
