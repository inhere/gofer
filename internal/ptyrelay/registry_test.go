package ptyrelay

import (
	"io"
	"sync"
	"testing"
	"time"
)

type registrySource struct {
	once   sync.Once
	done   chan struct{}
	mu     sync.Mutex
	closed bool
}

func newRegistrySource() *registrySource { return &registrySource{done: make(chan struct{})} }

func (s *registrySource) Read([]byte) (int, error) {
	<-s.done
	return 0, io.EOF
}
func (s *registrySource) Write(p []byte) (int, error) { return len(p), nil }
func (s *registrySource) Resize(int, int) error       { return nil }
func (s *registrySource) Close() error {
	s.once.Do(func() { close(s.done) })
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *registrySource) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func TestRegistryPrepareOpenCloseIdempotent(t *testing.T) {
	r := NewRegistry()
	b := RelayBinding{WorkerID: "w1", InstanceID: "i1", JobID: "j1", PtySessionID: "p1", Nonce: "n1", Expiry: 20}
	prepared := r.Prepare(b)
	if prepared == nil || prepared.State != RelayPendingWorker {
		t.Fatalf("Prepare state = %+v, want pending_worker", prepared)
	}
	if got, ok := r.Lookup("j1"); !ok || got.State != RelayPendingWorker || got.Binding != b {
		t.Fatalf("Lookup after Prepare = %+v,%v", got, ok)
	}
	if got, ok := r.LookupSession("p1"); !ok || got.Binding.JobID != "j1" {
		t.Fatalf("LookupSession after Prepare = %+v,%v", got, ok)
	}

	src := newRegistrySource()
	opened, err := r.Open("n1", src)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if opened.State != RelayOpen || opened.Relay == nil {
		t.Fatalf("Open entry = %+v, want open with relay", opened)
	}
	attached, err := r.MarkAttached("j1")
	if err != nil {
		t.Fatalf("MarkAttached returned error: %v", err)
	}
	if attached.State != RelayAttached {
		t.Fatalf("MarkAttached state = %s, want attached", attached.State)
	}
	if _, err := r.Open("n1", newRegistrySource()); err != ErrRelayBadNonce {
		t.Fatalf("second Open error = %v, want ErrRelayBadNonce", err)
	}

	r.Close("j1", "done")
	r.Close("j1", "again")
	if got, ok := r.Lookup("j1"); ok {
		t.Fatalf("Lookup after Close = %+v,true; want missing", got)
	}
	if got, ok := r.LookupSession("p1"); ok {
		t.Fatalf("LookupSession after Close = %+v,true; want missing", got)
	}
	if !src.isClosed() {
		t.Fatal("Close did not close source")
	}
}

func assertClosedChan(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("Done(%s) not immediately readable, want a pre-closed chan", name)
	}
}

// TestRegistryDoneStates covers the four Done edges (D-P2-2): open → the live
// relay.Done() (unclosed until the relay closes); pending / missing / finalized →
// the pre-closed sentinel so the host never blocks on a drain that cannot fire.
func TestRegistryDoneStates(t *testing.T) {
	r := NewRegistry()

	// missing job → pre-closed.
	assertClosedChan(t, r.Done("nope"), "missing")
	// nil receiver / empty id → pre-closed.
	assertClosedChan(t, (*Registry)(nil).Done("x"), "nil-registry")
	assertClosedChan(t, r.Done(""), "empty-id")

	// pending_worker (Prepared, not Opened) → pre-closed.
	r.Prepare(RelayBinding{JobID: "pend", PtySessionID: "pp", Nonce: "np"})
	assertClosedChan(t, r.Done("pend"), "pending")

	// open → live relay.Done(): NOT closed before the relay closes.
	r.Prepare(RelayBinding{JobID: "open", PtySessionID: "po", Nonce: "no"})
	if _, err := r.Open("no", newRegistrySource()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doneCh := r.Done("open")
	select {
	case <-doneCh:
		t.Fatal("Done(open) closed before relay Close — want a live drain signal")
	default:
	}
	r.Close("open", "test")
	select {
	case <-doneCh: // relay.Close closed r.done
	case <-time.After(2 * time.Second):
		t.Fatal("Done(open) did not close after relay Close")
	}
	// after Close the entry is detached → missing → pre-closed.
	assertClosedChan(t, r.Done("open"), "after-close")

	// finalized-but-still-indexed (defensive branch): Done still returns pre-closed.
	fin := &RelayEntry{Binding: RelayBinding{JobID: "fin"}, State: RelayFinalized}
	r.mu.Lock()
	r.byJob["fin"] = fin
	r.mu.Unlock()
	assertClosedChan(t, r.Done("fin"), "finalized-indexed")
}

// blockingCloseSource parks its recordLoop Read and blocks in Close until
// released, so a test can observe the registry mutex is NOT held while a slow
// source.Close runs (D-P2-8 lock-outside-close).
type blockingCloseSource struct {
	readBlock    chan struct{} // Read parks here (kept off the auto-EOF Close path)
	closeEntered chan struct{} // closed once Close is first entered
	closeRelease chan struct{} // Close blocks until this is closed
	onceEnter    sync.Once
}

func newBlockingCloseSource() *blockingCloseSource {
	return &blockingCloseSource{
		readBlock:    make(chan struct{}),
		closeEntered: make(chan struct{}),
		closeRelease: make(chan struct{}),
	}
}

func (s *blockingCloseSource) Read([]byte) (int, error)    { <-s.readBlock; return 0, io.EOF }
func (s *blockingCloseSource) Write(p []byte) (int, error) { return len(p), nil }
func (s *blockingCloseSource) Resize(int, int) error       { return nil }
func (s *blockingCloseSource) Close() error {
	s.onceEnter.Do(func() { close(s.closeEntered) })
	<-s.closeRelease
	return nil
}

// TestRegistryCloseLockOutside proves Close detaches under the lock but closes the
// relay OUTSIDE it: while a slow source.Close is in flight, a concurrent Lookup /
// MarkAttached of ANOTHER job returns promptly (no global HOL, D-P2-8).
func TestRegistryCloseLockOutside(t *testing.T) {
	r := NewRegistry()

	slow := newBlockingCloseSource()
	r.Prepare(RelayBinding{JobID: "slow", PtySessionID: "ps", Nonce: "ns"})
	if _, err := r.Open("ns", slow); err != nil {
		t.Fatalf("Open slow: %v", err)
	}
	r.Prepare(RelayBinding{JobID: "fast", PtySessionID: "pf", Nonce: "nf"})
	if _, err := r.Open("nf", newRegistrySource()); err != nil {
		t.Fatalf("Open fast: %v", err)
	}

	closeDone := make(chan struct{})
	go func() { r.Close("slow", "test"); close(closeDone) }()

	// Wait until we are inside the slow source.Close (registry lock already released).
	select {
	case <-slow.closeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close never reached the source Close")
	}

	// With the lock free, another job's Lookup + MarkAttached must not block.
	opDone := make(chan struct{})
	go func() {
		_, _ = r.Lookup("fast")
		_, _ = r.MarkAttached("fast")
		close(opDone)
	}()
	select {
	case <-opDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Lookup/MarkAttached blocked behind a slow source Close (HOL not eliminated)")
	}

	// Release the slow close and drain.
	close(slow.closeRelease)
	close(slow.readBlock)
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after release")
	}
}

// TestRegistryPrepareReplaceLockOutside proves Prepare's replacement of an
// existing job detaches the old relay under the lock and closes it OUTSIDE, so a
// slow old-source Close does not stall a concurrent Lookup of another job.
func TestRegistryPrepareReplaceLockOutside(t *testing.T) {
	r := NewRegistry()

	slow := newBlockingCloseSource()
	r.Prepare(RelayBinding{JobID: "j", PtySessionID: "p1", Nonce: "n1"})
	if _, err := r.Open("n1", slow); err != nil {
		t.Fatalf("Open: %v", err)
	}
	r.Prepare(RelayBinding{JobID: "other", PtySessionID: "po", Nonce: "no"})
	if _, err := r.Open("no", newRegistrySource()); err != nil {
		t.Fatalf("Open other: %v", err)
	}

	replaceDone := make(chan struct{})
	go func() {
		// Re-Prepare the same job → old relay (slow) is detached + closed outside lock.
		r.Prepare(RelayBinding{JobID: "j", PtySessionID: "p2", Nonce: "n2"})
		close(replaceDone)
	}()

	select {
	case <-slow.closeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Prepare replacement never reached the old source Close")
	}
	opDone := make(chan struct{})
	go func() { _, _ = r.Lookup("other"); close(opDone) }()
	select {
	case <-opDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Lookup blocked behind a slow replaced-relay Close (HOL)")
	}

	close(slow.closeRelease)
	close(slow.readBlock)
	select {
	case <-replaceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Prepare replacement did not return after release")
	}
}

// TestRegistryDetachIdempotent: repeated Close is safe (one source close), and
// detachLocked on a finalized / nil entry is a nil no-op.
func TestRegistryDetachIdempotent(t *testing.T) {
	r := NewRegistry()
	r.Prepare(RelayBinding{JobID: "j", PtySessionID: "p", Nonce: "n"})
	src := newRegistrySource()
	if _, err := r.Open("n", src); err != nil {
		t.Fatalf("Open: %v", err)
	}
	r.Close("j", "first")
	r.Close("j", "second") // idempotent, no panic / double close
	if !src.isClosed() {
		t.Fatal("Close did not close source")
	}

	fin := &RelayEntry{Binding: RelayBinding{JobID: "x"}, State: RelayFinalized}
	r.mu.Lock()
	gotFin := r.detachLocked(fin, "again")
	gotNil := r.detachLocked(nil, "x")
	r.mu.Unlock()
	if gotFin != nil {
		t.Fatalf("detachLocked(finalized) = %v, want nil", gotFin)
	}
	if gotNil != nil {
		t.Fatalf("detachLocked(nil) = %v, want nil", gotNil)
	}
}
