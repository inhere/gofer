package ptyrelay

import "sync"

// ring is a fixed-size byte ring buffer retaining the most recent N bytes of the
// pty output stream, for pre-attach scrollback (K6): a first viewer replays the
// tail so it does not start from a blank screen. Writes larger than the ring
// keep only the trailing size bytes. It is safe for concurrent Write/Snapshot.
type ring struct {
	mu      sync.Mutex
	buf     []byte
	size    int
	pos     int  // next write index
	full    bool // whether the buffer has wrapped
	written int  // total bytes ever written (monotonic)
}

func newRing(size int) *ring {
	if size <= 0 {
		size = defaultRingSize
	}
	return &ring{buf: make([]byte, size), size: size}
}

// Write appends p, retaining only the last size bytes.
func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	r.written += n
	if n >= r.size {
		// Only the trailing size bytes survive; reset to a linear full buffer.
		copy(r.buf, p[n-r.size:])
		r.pos = 0
		r.full = true
		return n, nil
	}
	// Copy in up to two segments across the wrap point.
	first := copy(r.buf[r.pos:], p)
	if first < n {
		copy(r.buf, p[first:])
	}
	r.pos = (r.pos + n) % r.size
	if r.pos == 0 || first < n {
		r.full = true
	}
	return n, nil
}

// Snapshot returns the retained bytes in chronological order.
func (r *ring) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}
	out := make([]byte, r.size)
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}

// WrittenTotal returns the total number of bytes ever written (not just
// retained), for progress assertions.
func (r *ring) WrittenTotal() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written
}
