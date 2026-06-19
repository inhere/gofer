package wshub

import (
	"context"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// workerConn is one live worker connection plus its register metadata and the
// set of per-job sinks the hub demuxes inbound frames to.
type workerConn struct {
	workerID string
	callerID string // authenticated identity (= the token-bound worker_id), review #1
	conn     *websocket.Conn
	meta     wsproto.Register

	// writeMu serialises outbound writes: coder/websocket requires a single
	// concurrent writer per connection.
	writeMu sync.Mutex

	mu    sync.Mutex
	sinks map[string]JobSink // job_id → sink (review #2 lifecycle)
}

// putSink registers a per-job sink (called before dispatch). registerSink is the
// hub-facing API; this is the per-conn primitive.
func (wc *workerConn) putSink(jobID string, sk JobSink) {
	wc.mu.Lock()
	wc.sinks[jobID] = sk
	wc.mu.Unlock()
}

func (wc *workerConn) deleteSink(jobID string) {
	wc.mu.Lock()
	delete(wc.sinks, jobID)
	wc.mu.Unlock()
}

// sink returns the sink for jobID, or nil when none is registered (a frame that
// arrives before the sink is registered, or after deregistration, is dropped —
// not a panic).
func (wc *workerConn) sink(jobID string) JobSink {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return wc.sinks[jobID]
}

// writeFrame marshals and sends one envelope under writeMu (single-writer).
func (wc *workerConn) writeFrame(ctx context.Context, t wsproto.FrameType, jobID string, payload any) error {
	wc.writeMu.Lock()
	defer wc.writeMu.Unlock()
	return wsjson.Write(ctx, wc.conn, wsproto.Envelope{
		Type:    t,
		JobID:   jobID,
		Payload: mustRaw(payload),
	})
}

// gracefulClose closes the connection with a normal-closure status and a reason.
// Used when a same-worker_id reconnect replaces an older connection (constraint
// #5) and on hub shutdown.
func (wc *workerConn) gracefulClose(reason string) {
	_ = wc.conn.Close(websocket.StatusNormalClosure, reason)
}

// WorkerRegistry maps worker_id → live connection, concurrency-safe.
type WorkerRegistry struct {
	mu    sync.RWMutex
	conns map[string]*workerConn
}

// newRegistry builds an empty registry.
func newRegistry() *WorkerRegistry {
	return &WorkerRegistry{conns: map[string]*workerConn{}}
}

// Put registers wc under its worker_id. If a connection already exists for that
// worker_id it is returned (old) so the caller can gracefully close it: a
// same-worker_id reconnect replaces the prior connection (constraint #5; the
// token binding already guarantees it is the same authenticated identity).
func (r *WorkerRegistry) Put(wc *workerConn) (old *workerConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old = r.conns[wc.workerID]
	r.conns[wc.workerID] = wc
	return old
}

// Get returns the live connection for workerID, if any.
func (r *WorkerRegistry) Get(workerID string) (*workerConn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wc, ok := r.conns[workerID]
	return wc, ok
}

// Remove deletes workerID's entry ONLY when the currently-registered connection
// is wc. This avoids a read-loop teardown for a replaced connection accidentally
// evicting the new connection that took its place.
func (r *WorkerRegistry) Remove(workerID string, wc *workerConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.conns[workerID]; ok && cur == wc {
		delete(r.conns, workerID)
	}
}
