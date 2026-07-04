package ptyrelay

import (
	"io"
	"sync"
	"testing"
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
	got, ok := r.Lookup("j1")
	if !ok {
		t.Fatal("Lookup after Close missing")
	}
	if got.State != RelayFinalized || got.CloseReason != "done" {
		t.Fatalf("closed entry = %+v, want finalized/done", got)
	}
	if !src.isClosed() {
		t.Fatal("Close did not close source")
	}
}
