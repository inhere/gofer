package wshub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/inhere/gofer/internal/wsproto"
)

// ErrFileXferUnsupported is returned when the target worker is connected and healthy
// but speaks a protocol older than the one carrying the file-transfer frames. Like
// ErrWorkerTooOld it is NOT a failure of the worker (it keeps running its jobs), and
// it must be surfaced as "upgrade and restart this worker" rather than retried: the
// transfer cannot be queued for a later version — the caller's file is either moved
// now or not at all.
var ErrFileXferUnsupported = errors.New("worker protocol too old for file transfer")

// SendFileXfer sends one transfer instruction to a worker and waits for its result.
//
// It is SYNCHRONOUS: the dispatch the caller is driving (a staged transfer) can only
// be settled done/failed by the worker's report, so the alternative to waiting is a
// record left dispatched until its timeout. The wait ends on exactly one of three
// events — the matching file_xfer_result, the connection dying, or ctx expiring — and
// the pending entry is removed on all of them (a waiter that leaves must never leak
// its slot). Nothing is queued or retried: a worker that is offline or too old fails
// the transfer with a reason the operator can act on (design §一.2).
//
// The frame carries NO job id (a transfer is not a job, exactly like tunnel_open), so
// the worker's read loop demuxes it by frame type, and the reply is correlated by
// XferID instead.
func (h *Hub) SendFileXfer(ctx context.Context, workerID string, req wsproto.FileXfer) (wsproto.FileXferResult, error) {
	if req.XferID == "" {
		return wsproto.FileXferResult{}, errors.New("file_xfer requires an xfer_id")
	}
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return wsproto.FileXferResult{}, ErrWorkerOffline
	}
	// Capability gate per CONNECTION (not a hub-wide version switch): a worker below
	// the floor is fully usable for jobs, it just has no frame to answer with, so
	// sending one would only strand the caller until the deadline.
	if !wc.supportsFileXfer() {
		return wsproto.FileXferResult{}, fmt.Errorf(
			"%w: worker %s speaks protocol v%d, file transfer needs v%d — upgrade and restart it",
			ErrFileXferUnsupported, workerID, wc.protocolVersion(), wsproto.FileXferMinProtocolVersion)
	}

	// Register the pending entry BEFORE the frame goes out: a worker can answer faster
	// than this goroutine is rescheduled, and a result that arrives before its waiter
	// exists would be dropped (or, worse, treated as an orphan by the fallback
	// handler's duplicate path).
	ch := wc.registerFileXfer(req.XferID)
	defer wc.deleteFileXfer(req.XferID)

	if err := wc.writeFrame(ctx, wsproto.TypeFileXfer, "", req); err != nil {
		return wsproto.FileXferResult{}, fmt.Errorf("send file_xfer frame to worker %s: %w", workerID, err)
	}

	started := h.nowFn()
	select {
	case res := <-ch:
		slog.Debug("worker.file_xfer_finished", "event", "worker.file_xfer_finished", "component", "server",
			"worker_id", workerID, "xfer_id", req.XferID, "op", req.Op, "ok", res.OK,
			"took", h.nowFn().Sub(started))
		return res, nil

	case <-wc.done:
		// The read loop exited (disconnect / read deadline / replacement). It may have
		// delivered the result on its way out, so prefer a delivered answer over the
		// disconnect — select picks a ready case at random when both are.
		if res, got := tryRecvFileXfer(ch); got {
			return res, nil
		}
		// A pending waiter FAILS HERE, immediately: the process is provably gone, and
		// waiting out the transfer deadline for it would only delay the operator's
		// "worker offline" by ten minutes.
		return wsproto.FileXferResult{}, ErrWorkerOffline

	case <-ctx.Done():
		if res, got := tryRecvFileXfer(ch); got {
			return res, nil
		}
		return wsproto.FileXferResult{}, ctx.Err()
	}
}

// SetFileXferResultHandler installs the fallback for a file_xfer_result that no
// dispatch is waiting for: the waiter already gave up (timeout, disconnect) or the
// record was settled elsewhere. The assembly (core) points it at the transfer
// manager, whose OnWorkerResult settles a record that is still dispatched and ignores
// one that is already terminal — so a late report can never reopen a finished
// transfer. A nil handler (hub used standalone, tests) simply drops such frames.
func (h *Hub) SetFileXferResultHandler(fn func(wsproto.FileXferResult)) {
	h.xferResMu.Lock()
	h.xferResFn = fn
	h.xferResMu.Unlock()
}

// fileXferResultHandler reads the installed fallback (called from the read loop).
func (h *Hub) fileXferResultHandler() func(wsproto.FileXferResult) {
	h.xferResMu.Lock()
	defer h.xferResMu.Unlock()
	return h.xferResFn
}

// registerFileXfer parks a 1-buffered result channel under xferID and returns it. The
// buffer is what lets the read loop hand off a result without ever blocking on a
// waiter that has already walked away (mirrors registerReload).
func (wc *workerConn) registerFileXfer(xferID string) chan wsproto.FileXferResult {
	ch := make(chan wsproto.FileXferResult, 1)
	wc.mu.Lock()
	if wc.pendingXfer == nil { // conns built outside newWorkerConn (tests) have no map yet
		wc.pendingXfer = map[string]chan wsproto.FileXferResult{}
	}
	wc.pendingXfer[xferID] = ch
	wc.mu.Unlock()
	return ch
}

// deleteFileXfer drops the pending entry for xferID (idempotent).
func (wc *workerConn) deleteFileXfer(xferID string) {
	wc.mu.Lock()
	delete(wc.pendingXfer, xferID)
	wc.mu.Unlock()
}

// resolveFileXfer hands a result to the waiter registered for its xfer id and reports
// whether there was one. An unknown xfer id (the dispatch already timed out, or a
// duplicate report) is NOT an error and never fatal: the read loop must keep serving
// every other frame on this connection — the caller of resolveFileXfer routes such a
// result to the fallback handler instead.
func (wc *workerConn) resolveFileXfer(res wsproto.FileXferResult) bool {
	wc.mu.Lock()
	ch := wc.pendingXfer[res.XferID]
	delete(wc.pendingXfer, res.XferID)
	wc.mu.Unlock()
	if ch == nil {
		return false
	}
	// Never blocks: the buffer holds one value and only the read loop ever sends.
	select {
	case ch <- res:
	default:
	}
	return true
}

// revokeFileXfers reclaims every pending transfer waiter on this connection. It runs
// from onDisconnect (after closeDone), so the waiters — which all select on wc.done —
// have already been woken; this only drops the map entries a dying connection would
// otherwise keep alive until their own timeouts.
func (wc *workerConn) revokeFileXfers() {
	wc.mu.Lock()
	wc.pendingXfer = nil
	wc.mu.Unlock()
}

// pendingFileXfers reports how many transfer waiters are still parked on this
// connection (leak assertions).
func (wc *workerConn) pendingFileXfers() int {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return len(wc.pendingXfer)
}

// tryRecvFileXfer takes a result out of ch if one is already buffered, without blocking.
func tryRecvFileXfer(ch chan wsproto.FileXferResult) (wsproto.FileXferResult, bool) {
	select {
	case res := <-ch:
		return res, true
	default:
		return wsproto.FileXferResult{}, false
	}
}
