package wshub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// ErrWorkerTooOld is returned when the target worker is connected and healthy but
// speaks a protocol older than the one that carries the reload frames. It is NOT a
// failure of the worker: it keeps running its jobs exactly as before, it just cannot
// be asked to re-read its config — the operator has to restart it after upgrading.
// Callers must surface it as such (a "restart me" prompt), never as a server fault.
var ErrWorkerTooOld = errors.New("worker protocol too old to reload config")

// ErrReloadTimeout is returned when the worker acknowledged nothing within the
// caller's deadline. The reload is NOT known to have failed — the worker may still
// apply it — so the caller must report "no receipt in time", not "reload failed".
var ErrReloadTimeout = errors.New("worker did not answer the reload request in time")

// ReloadRejected is returned when the worker answered but REFUSED the new config
// (bad YAML, unknown agent, …). It is the good case of a bad outcome: the worker is
// alive, still running its previous config, and told us exactly why. Reason is the
// worker's own message and must reach the user verbatim — swallowing it is how a
// broken config turns into a silent no-op.
type ReloadRejected struct {
	WorkerID string
	Reason   string
}

func (e *ReloadRejected) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("worker %s rejected the config reload", e.WorkerID)
	}
	return fmt.Sprintf("worker %s rejected the config reload: %s", e.WorkerID, e.Reason)
}

// ReloadWorker asks one worker to re-read its local config and waits for the answer.
//
// It is SYNCHRONOUS on purpose: the caller is holding an HTTP request open on the
// outcome, and "202 accepted" for an operation that can fail on the far side (a
// syntax error in the worker's config) is how a failed reload becomes an invisible
// one. The whole request/receipt correlation lives here, in the hub, because the
// ReloadResult frame arrives on the hub's per-connection read loop — the layer above
// (serve/httpapi) only maps the returned error onto a status code.
//
// The wait ends on exactly one of four events:
//   - the matching ReloadResult arrives → applied (Caps already stored, see readLoop)
//     or ReloadRejected with the worker's reason;
//   - the worker disconnects → ErrWorkerOffline immediately. Waiting out the full
//     deadline for a process that is provably gone is a bug, not caution;
//   - ctx expires → ErrReloadTimeout, and the pending entry is removed (a waiter that
//     leaves must never leak its slot);
//   - ctx is cancelled (client hung up) → ctx.Err().
//
// Errors: ErrWorkerOffline (not connected / dropped mid-wait), ErrWorkerTooOld
// (protocol below wsproto.ReloadMinProtocolVersion), *ReloadRejected (worker said no),
// ErrReloadTimeout (no receipt), or a write error. On success the returned Caps is the
// worker's new capability snapshot — already applied to the registry BEFORE this
// returns, so a caller that reads /v1/meta right after can never see the stale view.
func (h *Hub) ReloadWorker(ctx context.Context, workerID, reason string) (wsproto.Caps, error) {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return wsproto.Caps{}, ErrWorkerOffline
	}
	// Capability gate, per connection (not a hub-wide version switch): a worker below
	// the reload floor is fully usable, it just has no frame to answer with — sending
	// one would strand the caller until the deadline for no reason.
	if !wc.supportsReload() {
		return wsproto.Caps{}, fmt.Errorf("%w: worker %s speaks protocol v%d, config reload needs v%d — upgrade and restart it",
			ErrWorkerTooOld, workerID, wc.protocolVersion(), wsproto.ReloadMinProtocolVersion)
	}

	reqID := newRequestID()
	ch := wc.registerReload(reqID)
	// The waiter owns its slot for the whole call and always reclaims it — on success,
	// timeout, disconnect and write failure alike.
	defer wc.deleteReload(reqID)

	// Register the pending entry BEFORE the frame goes out: a worker can answer faster
	// than this goroutine is rescheduled, and a receipt that arrives before its waiter
	// exists would be dropped as unknown.
	if err := wc.writeFrame(ctx, wsproto.TypeReload, "", wsproto.Reload{RequestID: reqID, Reason: reason}); err != nil {
		return wsproto.Caps{}, fmt.Errorf("send reload frame to worker %s: %w", workerID, err)
	}
	slog.Info("hub asked a worker to reload its config",
		"worker_id", workerID, "request_id", reqID, "reason", reason)

	started := h.nowFn()
	select {
	case rr := <-ch:
		return h.reloadOutcome(workerID, reqID, started, rr)

	case <-wc.done:
		// The read loop exited (disconnect / read deadline / replacement). It may have
		// delivered the receipt on its way out, so prefer a delivered answer over the
		// disconnect — select picks a ready case at random when both are.
		if rr, got := tryRecvReload(ch); got {
			return h.reloadOutcome(workerID, reqID, started, rr)
		}
		slog.Warn("hub reload aborted: worker disconnected while waiting for the receipt",
			"worker_id", workerID, "request_id", reqID)
		return wsproto.Caps{}, ErrWorkerOffline

	case <-ctx.Done():
		if rr, got := tryRecvReload(ch); got {
			return h.reloadOutcome(workerID, reqID, started, rr)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			slog.Warn("hub reload timed out waiting for the worker's receipt",
				"worker_id", workerID, "request_id", reqID, "waited", h.nowFn().Sub(started))
			return wsproto.Caps{}, fmt.Errorf("%w: worker %s (request %s)", ErrReloadTimeout, workerID, reqID)
		}
		return wsproto.Caps{}, ctx.Err()
	}
}

// reloadOutcome maps a received receipt onto the (Caps, error) contract. The registry
// has already been updated by the read loop at this point (see readLoop's
// TypeReloadResult case) — this only decides what the CALLER is told.
func (h *Hub) reloadOutcome(workerID, reqID string, started time.Time, rr wsproto.ReloadResult) (wsproto.Caps, error) {
	if !rr.OK {
		slog.Warn("worker rejected the config reload (still running its previous config)",
			"worker_id", workerID, "request_id", reqID, "err", rr.Err)
		return wsproto.Caps{}, &ReloadRejected{WorkerID: workerID, Reason: rr.Err}
	}
	slog.Info("worker applied the config reload",
		"worker_id", workerID, "request_id", reqID, "took", h.nowFn().Sub(started))
	if rr.Caps == nil {
		// OK with no caps: nothing to report and nothing was applied to the registry
		// (an absent snapshot is NOT an empty one — see UpdateCaps). Succeed with the
		// zero value rather than wiping the worker's capabilities on a malformed frame.
		return wsproto.Caps{}, nil
	}
	return *rr.Caps, nil
}

// registerReload parks a 1-buffered receipt channel under reqID and returns it. The
// buffer is what lets the read loop hand off a receipt without ever blocking on a
// waiter that has already walked away.
func (wc *workerConn) registerReload(reqID string) chan wsproto.ReloadResult {
	ch := make(chan wsproto.ReloadResult, 1)
	wc.mu.Lock()
	if wc.pending == nil { // conns built outside newWorkerConn (tests) have no map yet
		wc.pending = map[string]chan wsproto.ReloadResult{}
	}
	wc.pending[reqID] = ch
	wc.mu.Unlock()
	return ch
}

// deleteReload drops the pending entry for reqID (idempotent).
func (wc *workerConn) deleteReload(reqID string) {
	wc.mu.Lock()
	delete(wc.pending, reqID)
	wc.mu.Unlock()
}

// resolveReload hands a receipt to the waiter registered for its request_id. Two
// things it deliberately tolerates rather than treats as errors:
//   - an UNKNOWN request_id (a receipt for a request that already timed out, or one
//     this hub never sent) — dropped;
//   - a waiter that has already left — the send is non-blocking into the buffer, and
//     the channel is never closed, so a late receipt can neither panic nor wedge the
//     read loop (which must keep serving every other job on this connection).
func (wc *workerConn) resolveReload(rr wsproto.ReloadResult) {
	wc.mu.Lock()
	ch := wc.pending[rr.RequestID]
	delete(wc.pending, rr.RequestID)
	wc.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- rr:
	default: // buffer already holds a receipt: the first one wins, this is a duplicate
	}
}

// pendingReloads reports how many reload requests are still waiting on this
// connection (leak assertions).
func (wc *workerConn) pendingReloads() int {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return len(wc.pending)
}

// tryRecvReload takes a receipt out of ch if one is already buffered, without
// blocking.
func tryRecvReload(ch chan wsproto.ReloadResult) (wsproto.ReloadResult, bool) {
	select {
	case rr := <-ch:
		return rr, true
	default:
		return wsproto.ReloadResult{}, false
	}
}

// newRequestID mints the correlation id for one reload RPC: 16 crypto/rand bytes as
// hex, the same shape the rest of the codebase uses for connection nonces and session
// ids (no uuid dependency in this工具库). It only has to be unique among the requests
// in flight on one connection; an RNG failure (never in practice) degrades to a
// time-derived value rather than a panic, which keeps a reload from taking the serve
// process down.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
