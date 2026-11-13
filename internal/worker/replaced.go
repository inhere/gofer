package worker

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// replacedCloseReason is the websocket close reason the hub sends to a connection
// that another registration under the same worker_id has taken over.
const replacedCloseReason = "replaced by new registration"

const (
	// replacedLimit replacements within replacedWindow make this worker back off for
	// replacedPause instead of reconnecting at once: two processes sharing a
	// worker_id would otherwise evict each other without end.
	replacedLimit  = 3
	replacedWindow = 5 * time.Minute
	replacedPause  = 5 * time.Minute
)

// replacedTracker counts how often the hub replaced this worker's connection. The
// zero value is ready to use (real clock, default limits).
type replacedTracker struct {
	mu    sync.Mutex
	times []time.Time
	now   func() time.Time
}

// note records one replacement and reports the pause to apply before reconnecting
// (0 when under the limit). After a pause is returned the count starts over.
func (t *replacedTracker) note() (n int, pause time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	kept := t.times[:0]
	for _, at := range t.times {
		if now.Sub(at) < replacedWindow {
			kept = append(kept, at)
		}
	}
	t.times = append(kept, now)
	n = len(t.times)
	if n >= replacedLimit {
		t.times = nil
		return n, replacedPause
	}
	return n, 0
}

// isReplacedClose reports whether err is the hub's "replaced by new registration" close.
func isReplacedClose(err error) bool {
	var ce websocket.CloseError
	return errors.As(err, &ce) && ce.Reason == replacedCloseReason
}

// noteReplaced reacts to a session that ended with err: when the hub replaced this
// connection (and it is not the upgrade handover, held=true, where the replacement
// is expected) it logs a clear error and returns how long to wait before dialling
// again. It never ends the process.
func (cl *Client) noteReplaced(err error, held bool) time.Duration {
	if held || !isReplacedClose(err) {
		return 0
	}
	n, pause := cl.replaced.note()
	slog.Error("worker.replaced", "event", "worker.replaced", "component", "worker",
		"worker_id", cl.workerID, "instance_id", cl.instanceID, "replaced_count", n,
		"msg", "another process is connected with the same worker_id; this connection was replaced")
	if pause > 0 {
		slog.Error("worker.replaced_backoff", "event", "worker.replaced_backoff", "component", "worker",
			"worker_id", cl.workerID, "instance_id", cl.instanceID, "pause_sec", int(pause.Seconds()),
			"msg", "replaced repeatedly; waiting before reconnecting (is a second worker running with this worker_id?)")
	}
	return pause
}
