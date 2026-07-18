// Package ptyrelay is the serve-side per-session pty relay (WEB-03 design §6,
// K2/K6): it consumes a PtySource (the raw byte stream from a worker's pty over
// the dedicated pty ws, or a serve-local ptmx) and drives TWO independent
// backpressure layers:
//
//   - recorder MAIN path (bounded, must not lose bytes): every chunk read from
//     the source is appended to a bounded ring buffer (pre-attach scrollback)
//     and an optional cast sink. This path is synchronous and never gated on any
//     viewer.
//   - viewer FAN-OUT (each viewer its own bounded queue): a slow viewer drops
//     screen refreshes (or is disconnected) and NEVER blocks the recorder or a
//     fast viewer.
//
// It also holds the single input WRITE lease (K3): the first attaching writer
// owns exclusive stdin; a second writer is refused; read-only followers never
// take the lease.
//
// This package is a leaf (stdlib only): the transport (ws) and the source impls
// live above it; it depends on neither job nor runner (G022).
package ptyrelay

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Errors returned by the relay's admission paths.
var (
	// ErrLeaseTaken is returned when a second writer tries to attach while the
	// input write lease is already held (K3 exclusive write).
	ErrLeaseTaken = errors.New("ptyrelay: input write lease already held")
	// ErrReadOnly is returned when a read-only viewer tries to send input.
	ErrReadOnly = errors.New("ptyrelay: viewer is read-only (no write lease)")
	// ErrClosed is returned by operations on a closed relay.
	ErrClosed = errors.New("ptyrelay: relay is closed")
)

// PtySource is the raw byte stream a Relay consumes. remotePtySource (worker pty
// ws, V1) and localPtySource (serve ptmx, drop-in) both satisfy it; the relay is
// transport-agnostic.
type PtySource interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(cols, rows int) error
	Close() error
}

// defaults for the two backpressure layers.
const (
	defaultRingSize    = 64 * 1024 // pre-attach scrollback retained (bytes)
	defaultViewerQueue = 64        // per-viewer bounded output queue (chunks)
	readChunk          = 4096
)

// castCloseGrace bounds how long finish() waits for the cast sink's Close to
// return before giving up (the recording is then considered failed by the
// concrete sink; Done still fires so the host is never blocked indefinitely).
// var (not const) so tests can shrink it.
var castCloseGrace = 2 * time.Second

// Relay is one pty session's serve-side relay + state.
type Relay struct {
	src  PtySource
	cast CastSink // optional cast sink; nil = no recording
	obs  OutputObserver

	mu          sync.Mutex
	ring        *ring
	viewers     map[int]*Viewer
	nextID      int
	leaseHolder int // viewer id holding the write lease; 0 = free
	closed      bool
	started     bool
	// cols/rows is the CURRENT pty size — the single size truth every viewer must
	// follow (tools-3xy). Seeded from the binding's initial size at Open, updated
	// on every successful Resize; 0 means "never known" (hello falls back).
	cols, rows int

	bytesIn     atomic.Int64 // total stdin bytes forwarded to the source (D-P3-8)
	castClean   atomic.Bool  // true = no cast OR cast封尾 cleanly within grace; false = Close timed out (H1)
	viewerQueue int
	done        chan struct{}
	finishOnce  sync.Once // close owner: finish() runs exactly once (B1)
}

// CastSink is the recorder main path's optional second consumer (asciinema-style
// cast recording). ptyrelay only Writes chunks and Closes the sink on finish; it
// is deliberately narrow (io.Writer + Close, no Err) so ptyrelay stays a leaf —
// the concrete sink (castrec) lives above and satisfies this; the httpapi
// finalize path consults the concrete sink's richer state (Err) directly (H5).
type CastSink interface {
	io.Writer
	Close() error
}

// Option configures a Relay.
type Option func(*Relay)

// WithRingSize sets the pre-attach scrollback ring size in bytes.
func WithRingSize(n int) Option { return func(r *Relay) { r.ring = newRing(n) } }

// WithViewerQueue sets the per-viewer bounded output-queue depth (chunks).
func WithViewerQueue(n int) Option { return func(r *Relay) { r.viewerQueue = n } }

// WithCast sets the recorder's cast sink (raw byte recording).
func WithCast(w CastSink) Option { return func(r *Relay) { r.cast = w } }

// OutputObserver receives every output chunk after the recorder's ring/cast
// write. It is for tiny metadata extraction, e.g. detecting an agent session id;
// implementations must not block the relay.
type OutputObserver func([]byte)

// WithOutputObserver wires a non-owning observer for pty output chunks.
func WithOutputObserver(fn OutputObserver) Option { return func(r *Relay) { r.obs = fn } }

// New builds a Relay over src. Call Start once to begin recording.
func New(src PtySource, opts ...Option) *Relay {
	r := &Relay{
		src:         src,
		ring:        newRing(defaultRingSize),
		viewers:     map[int]*Viewer{},
		viewerQueue: defaultViewerQueue,
		done:        make(chan struct{}),
	}
	// Default clean (H1): no cast, or a cast that封尾s within grace, is "clean";
	// only a boundedCastClose timeout flips this to false.
	r.castClean.Store(true)
	for _, o := range opts {
		o(r)
	}
	return r
}

// Start launches the recorder goroutine (idempotent).
func (r *Relay) Start() {
	r.mu.Lock()
	if r.started || r.closed {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()
	go r.recordLoop()
}

// recordLoop is the recorder MAIN path: read source → ring + cast → fan-out.
// It is never gated on any viewer (fan-out is non-blocking), so a slow viewer
// cannot stall recording (K2 layer 1). On source EOF/error it closes the relay.
//
// It is the sole close OWNER of the cast sink + done (B1): the deferred finish()
// runs AFTER the read loop has exited, so cast.Close never races the cast.Write
// calls made in this loop. Close() (external teardown) only closes the source /
// viewers and — because it unblocks src.Read — lets this loop return into finish.
func (r *Relay) recordLoop() {
	defer r.finish()
	buf := make([]byte, readChunk)
	for {
		n, err := r.src.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			// Main path (must not drop): ring + cast are synchronous, fast sinks.
			r.ring.Write(chunk)
			if r.cast != nil {
				_, _ = r.cast.Write(chunk)
			}
			if r.obs != nil {
				r.obs(chunk)
			}
			// Fan-out (may drop): each viewer's own bounded queue.
			r.fanout(chunk)
		}
		if err != nil {
			r.Close()
			return
		}
	}
}

// fanout delivers chunk to each viewer's bounded queue WITHOUT blocking: a
// viewer whose queue is full is marked lagged and the chunk is dropped for it
// (K2 layer 2), so neither the recorder nor a fast viewer waits on a slow one.
func (r *Relay) fanout(chunk []byte) {
	r.mu.Lock()
	vs := make([]*Viewer, 0, len(r.viewers))
	for _, v := range r.viewers {
		vs = append(vs, v)
	}
	r.mu.Unlock()
	for _, v := range vs {
		select {
		case v.out <- chunk:
		default:
			v.markLagged()
		}
	}
}

// Scrollback returns a copy of the ring's currently retained bytes (the
// pre-attach tail a first viewer replays, K6).
func (r *Relay) Scrollback() []byte { return r.ring.Snapshot() }

// RecordedLen reports how many bytes the recorder has taken from the source (for
// tests to observe main-path progress deterministically).
func (r *Relay) RecordedLen() int { return r.ring.WrittenTotal() }

// AddViewer registers a viewer with the relay's default per-viewer queue depth.
// write=true tries to take the exclusive input lease (K3): the first writer
// wins; a second writer is refused with ErrLeaseTaken. Read-only viewers never
// touch the lease.
func (r *Relay) AddViewer(write bool) (*Viewer, error) {
	return r.AddViewerWithQueue(write, r.viewerQueue)
}

// AddViewerWithQueue is AddViewer with an explicit per-viewer output-queue depth
// (chunks). The queue is what bounds a viewer independently of others: a viewer
// with a shallow queue that stops draining laggs and drops, without affecting a
// deeper/faster viewer or the recorder (K2 layer 2).
func (r *Relay) AddViewerWithQueue(write bool, queue int) (*Viewer, error) {
	if queue <= 0 {
		queue = r.viewerQueue
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	if write && r.leaseHolder != 0 {
		return nil, ErrLeaseTaken
	}
	r.nextID++
	v := &Viewer{
		id:    r.nextID,
		relay: r,
		write: write,
		out:   make(chan []byte, queue),
	}
	if write {
		r.leaseHolder = v.id
	}
	r.viewers[v.id] = v
	return v, nil
}

// removeViewer drops a viewer and releases the lease if it held it.
func (r *Relay) removeViewer(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leaseHolder == id {
		r.leaseHolder = 0
	}
	if v, ok := r.viewers[id]; ok {
		v.closeOut()
		delete(r.viewers, id)
	}
}

// seedSize records the pty's initial size (the binding's cols/rows) so Size and
// the size broadcast start from truth before any Resize happens. Zero values are
// ignored (size stays "unknown" and consumers fall back).
func (r *Relay) seedSize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	r.mu.Lock()
	r.cols, r.rows = cols, rows
	r.mu.Unlock()
}

// Size returns the current pty size ((0,0) when never known).
func (r *Relay) Size() (cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cols, r.rows
}

// Resize forwards a window-size change to the source (a writer-lease viewer's
// resize; the transport gates WHO may resize — see attach handler — the relay
// gates nothing beyond the source's own validation).
//
// On success it records the new size and notifies every viewer's size listener
// (tools-3xy): the pty size is a single shared truth, and a viewer that misses a
// change has no way to recover (its terminal stays laid out for a size the TUI
// no longer draws for). Listeners are invoked on their own goroutines so a slow
// viewer connection can never stall the resizing client's read loop.
func (r *Relay) Resize(cols, rows int) error {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err := r.src.Resize(cols, rows); err != nil {
		return err
	}
	r.mu.Lock()
	r.cols, r.rows = cols, rows
	fns := make([]func(int, int), 0, len(r.viewers))
	for _, v := range r.viewers {
		if fn := v.sizeListener(); fn != nil {
			fns = append(fns, fn)
		}
	}
	r.mu.Unlock()
	for _, fn := range fns {
		go fn(cols, rows)
	}
	return nil
}

// Close tears down the relay: closes the source, drops all viewers. Idempotent —
// all five close sources (source EOF, worker disconnect, browser close, cancel,
// explicit) funnel here (design §5 unified CAS close).
//
// Close is NOT the close owner of done/cast (B1): when the relay was Start()ed,
// closing the source unblocks the recordLoop's src.Read, and the recordLoop's
// deferred finish() closes the cast sink (bounded) then done — so cast.Close
// never races the recordLoop's cast.Write. Only a relay that was NEVER Start()ed
// (no recordLoop to run finish) closes them here.
func (r *Relay) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	started := r.started
	vs := r.viewers
	r.viewers = map[int]*Viewer{}
	r.leaseHolder = 0
	r.mu.Unlock()

	for _, v := range vs {
		v.closeOut()
	}
	err := r.src.Close()
	if !started {
		// Never Start()ed: no recordLoop exists to own finish, so close cast+done
		// here. Safe from a cast.Write race because recordLoop (the only Writer)
		// was never launched. finishOnce keeps it single even if Start races in.
		r.finish()
	}
	return err
}

// finish is the single close OWNER of the cast sink and done (B1): it closes the
// cast (bounded) then done, exactly once. It runs from the recordLoop's defer
// (started relays) or from Close (never-started relays) — never concurrently with
// a cast.Write, so the recording tail is intact.
func (r *Relay) finish() {
	r.finishOnce.Do(func() {
		if r.cast != nil {
			r.boundedCastClose()
		}
		close(r.done)
	})
}

// boundedCastClose closes the cast sink but never blocks finish (hence Done)
// longer than castCloseGrace: a wedged sink Close is abandoned. When it times
// out the cast was NOT sealed cleanly, so castClean is flipped to false (H1) —
// the finalize path (httpapi) reads this via CastClosedCleanly() and blanks the
// recording_uri, since sink.Err() alone can't observe a still-wedged Close. The
// host waiting on Done stays bounded (P2 host-grace invariant preserved).
func (r *Relay) boundedCastClose() {
	cdone := make(chan struct{})
	go func() {
		_ = r.cast.Close()
		close(cdone)
	}()
	select {
	case <-cdone:
	case <-time.After(castCloseGrace):
		r.castClean.Store(false) // timeout → cast not封尾 → recording is failed
	}
}

// CastClosedCleanly reports whether the cast recording was sealed cleanly. It is
// true when there was no cast at all or the sink's Close returned within
// castCloseGrace; it is false only when boundedCastClose timed out (a wedged
// Close, tail not封尾). Only meaningful after Done() has fired (H1).
func (r *Relay) CastClosedCleanly() bool { return r.castClean.Load() }

// InputLen reports the total stdin bytes forwarded to the source across all
// write-lease viewers (D-P3-8, for the pty_sessions bytes_in column).
func (r *Relay) InputLen() int64 { return r.bytesIn.Load() }

// Done is closed by finish() — after the recordLoop has exited (the pty output
// tail is recorded) and the cast sink is封尾 (or its Close timed out). For a
// relay that was never Start()ed, Close() closes it. Either way it always fires.
func (r *Relay) Done() <-chan struct{} { return r.done }

// Viewer is one attached consumer of the session. Output arrives on Out();
// SendInput forwards stdin bytes to the source ONLY if this viewer holds the
// write lease.
type Viewer struct {
	id    int
	relay *Relay
	write bool
	out   chan []byte

	mu       sync.Mutex
	lagged   bool
	outClosd bool
	sizeFn   func(cols, rows int) // optional size-change listener (tools-3xy)
}

// Out is the viewer's bounded output stream. It is closed when the viewer is
// removed or the relay closes.
func (v *Viewer) Out() <-chan []byte { return v.out }

// SendInput forwards raw stdin bytes to the source. Only the write-lease holder
// may write; a read-only follower is refused (K3).
func (v *Viewer) SendInput(b []byte) error {
	if !v.write {
		return ErrReadOnly
	}
	v.relay.mu.Lock()
	closed := v.relay.closed
	hasLease := v.relay.leaseHolder == v.id
	v.relay.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if !hasLease {
		return ErrReadOnly
	}
	n, err := v.relay.src.Write(b)
	if n > 0 {
		v.relay.bytesIn.Add(int64(n)) // D-P3-8: count forwarded stdin bytes
	}
	return err
}

// HoldsLease reports whether this viewer currently holds the exclusive input
// write lease. The transport uses it to gate resize the same way SendInput gates
// stdin (tools-3xy R3): only the writer may change the shared pty size.
func (v *Viewer) HoldsLease() bool {
	v.relay.mu.Lock()
	defer v.relay.mu.Unlock()
	return v.relay.leaseHolder == v.id
}

// SetSizeListener registers fn to be called (on its own goroutine) after every
// successful Resize, with the new pty size. One listener per viewer; nil clears.
func (v *Viewer) SetSizeListener(fn func(cols, rows int)) {
	v.mu.Lock()
	v.sizeFn = fn
	v.mu.Unlock()
}

func (v *Viewer) sizeListener() func(cols, rows int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.sizeFn
}

// Lagged reports whether this viewer has dropped at least one chunk because its
// queue was full (a slow-consumer signal the transport uses to disconnect it).
func (v *Viewer) Lagged() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lagged
}

// Close detaches the viewer (releases the lease if held).
func (v *Viewer) Close() { v.relay.removeViewer(v.id) }

func (v *Viewer) markLagged() {
	v.mu.Lock()
	v.lagged = true
	v.mu.Unlock()
}

func (v *Viewer) closeOut() {
	v.mu.Lock()
	if !v.outClosd {
		v.outClosd = true
		close(v.out)
	}
	v.mu.Unlock()
}
