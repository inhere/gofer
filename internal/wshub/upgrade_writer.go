// Package wshub hosts the hub-side WebSocket transport for the ws-worker effort
// (main plan §4): a serve-process singleton that accepts worker connections on
// GET /v1/workers/connect, runs a single per-connection read loop, demuxes
// inbound frames by job_id and dispatches jobs to workers. See P1-wp1-core-plan.
package wshub

import (
	"bufio"
	"net"
	"net/http"
)

// wsUpgradeWriter adapts rux's response writer (c.Resp) so coder/websocket's
// Accept can complete the WS upgrade. This is the P0 hard finding promoted from
// the spike (internal/wshub/spike_test.go, verified with +/- origin and -race):
//
// rux's *responseWriter BUFFERS WriteHeader — it only records the status and the
// real flush happens at end-of-chain in ensureWriteHeader, which Hijack() turns
// into a no-op. A WS upgrade requires the 101 status line to reach the socket
// BEFORE Hijack detaches the connection. If c.Resp is passed raw to Accept, the
// 101 stays buffered, is never sent, and the client's Dial silently hangs.
//
// The adapter forces the recorded status to flush on WriteHeader (by writing a
// zero-length body, which calls rux's ensureWriteHeader) and otherwise delegates
// Header/Hijack/Flush to rux's wrapper — preserving its Hijack/Flush passthrough
// and the no-double-write invariant (once Hijack runs, rux's Written() is true so
// its end-of-chain ensureWriteHeader is a no-op).
//
// Production WP1 shape: hub.Accept wraps c.Resp in wsUpgradeWriter before calling
// websocket.Accept. NEVER pass c.Resp raw to Accept.
type wsUpgradeWriter struct {
	rw          http.ResponseWriter // rux's c.Resp (*responseWriter)
	wroteHeader bool
}

func (w *wsUpgradeWriter) Header() http.Header { return w.rw.Header() }

func (w *wsUpgradeWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	// Record the status on the rux wrapper, then force an immediate flush by
	// writing a zero-length body: rux's Write() calls ensureWriteHeader(), which
	// emits the recorded status (101) to the underlying writer right now — before
	// Accept hijacks. Without this the 101 stays buffered and is lost.
	w.rw.WriteHeader(status)
	_, _ = w.rw.Write(nil)
}

func (w *wsUpgradeWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.rw.Write(b)
}

// Hijack delegates to rux's wrapper, which forwards to the underlying
// http.Hijacker and flips its Written() to true (no-double-write invariant).
func (w *wsUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.rw.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

func (w *wsUpgradeWriter) Flush() {
	if f, ok := w.rw.(http.Flusher); ok {
		f.Flush()
	}
}
