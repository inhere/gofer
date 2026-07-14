package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// ReloadFunc re-reads the worker's config from disk and applies it, returning the
// capability snapshot derived from the SAME config it applied.
//
// It is INJECTED by the command layer (only it knows where worker.yaml lives and
// how to map it onto a config.Config); the worker package owns the orchestration
// around it (queueing, serialisation, receipts) — see G021.
//
// Contract the implementation MUST honour (this is what makes "a bad config keeps
// the old one" true): build and validate the new config COMPLETELY first, and only
// then apply it (core.ReloadWith). Any failure — unreadable file, bad YAML, failed
// validation — must return an error BEFORE anything is applied, leaving the worker
// running its previous config untouched. The apply stage cannot fail, so it cannot
// roll back. The returned Caps must be derived from the very config that was
// applied (what we advertise is exactly what we will accept).
type ReloadFunc func() (wsproto.Caps, error)

// reloadReq is one queued reload. requestID is the hub's Reload.RequestID for a
// remote request (answered with a reload_result frame); it is EMPTY for a local
// SIGHUP, which has no requester and is answered with an unsolicited caps frame
// instead.
type reloadReq struct {
	requestID string
	reason    string
}

const (
	// reloadQueueCap bounds the reload queue. Reloads are cheap and rare (an
	// operator action), so a handful of slots absorb any realistic burst; the cap
	// exists so a misbehaving hub cannot make the worker buffer requests without
	// limit. An overflowing request is REJECTED with a busy receipt, never dropped
	// silently (see enqueueReload).
	reloadQueueCap = 8

	// reloadWriteTimeout bounds a single reload-related frame write (reload_result /
	// caps). The reload executor is a SINGLE goroutine, so a write onto a half-open
	// socket must not park it forever — that would stall every later reload behind
	// it. It is well under DefaultReadDeadline (45s), which stays the authoritative
	// disconnect detector: a timed-out write just abandons this receipt, and the recv
	// loop tears the session down on its own schedule.
	reloadWriteTimeout = 5 * time.Second
)

// enqueueReload hands one reload request to the serial executor without blocking
// the caller (the recv loop / the signal goroutine). It reports whether the request
// was queued; a full queue returns false so the caller can answer "busy" — a remote
// request MUST get a receipt, it is never dropped silently.
func (cl *Client) enqueueReload(req reloadReq) bool {
	select {
	case cl.reloadCh <- req:
		return true
	default:
		return false
	}
}

// onReload handles an inbound reload frame from the hub. It ONLY enqueues (and
// answers busy when the queue is full): the actual reload runs on the executor
// goroutine, so the read loop is never blocked by it, and no per-request goroutine
// is spawned — concurrent reloads would otherwise apply out of order and an older
// config could win over a newer one.
func (cl *Client) onReload(ctx context.Context, r wsproto.Reload) {
	if cl.enqueueReload(reloadReq{requestID: r.RequestID, reason: r.Reason}) {
		return
	}
	slog.Warn("worker reload queue full, rejecting request",
		"worker_id", cl.workerID, "request_id", r.RequestID, "reason", r.Reason)
	if r.RequestID == "" {
		return
	}
	cl.writeReloadFrame(ctx, wsproto.TypeReloadResult, wsproto.ReloadResult{
		RequestID: r.RequestID,
		OK:        false,
		Err:       "worker busy: reload queue is full",
	})
}

// reloadLoop is the worker's SERIAL reload executor: one goroutine consuming one
// queue, so reloads are applied strictly in arrival order (the last request to
// arrive is the last one applied — a stale config can never win a race) and only
// one config is ever being applied at a time. It is started once per Client by Run
// and lives across reconnects; ctx is the worker's lifetime ctx.
//
// A remote request (requestID != "") is answered with a reload_result carrying the
// outcome — including the failure, so the caller learns the config was refused
// instead of being told "accepted". A local SIGHUP has nobody to answer, so a
// successful one re-reports the new capabilities with an unsolicited caps frame; a
// failed one is logged (the old config keeps running, the worker does not exit).
func (cl *Client) reloadLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-cl.reloadCh:
			cl.runReload(ctx, req)
		}
	}
}

// runReload executes exactly one queued reload (always on the executor goroutine).
func (cl *Client) runReload(ctx context.Context, req reloadReq) {
	caps, err := cl.doReload()
	if err == nil {
		// Both paths update the advertised snapshot, so a later reconnect registers
		// with the CURRENT capabilities, not the ones the process booted with.
		cl.storeCaps(caps)
	}
	switch {
	case req.requestID != "":
		res := wsproto.ReloadResult{RequestID: req.requestID, OK: err == nil}
		if err != nil {
			slog.Error("worker reload failed, keeping old config",
				"worker_id", cl.workerID, "request_id", req.requestID, "err", err)
			res.Err = err.Error()
		} else {
			slog.Info("worker config reloaded",
				"worker_id", cl.workerID, "request_id", req.requestID, "reason", req.reason)
			c := caps
			res.Caps = &c
		}
		cl.writeReloadFrame(ctx, wsproto.TypeReloadResult, res)
	case err != nil:
		slog.Error("worker reload failed, keeping old config",
			"worker_id", cl.workerID, "reason", req.reason, "err", err)
	default:
		slog.Info("worker config reloaded", "worker_id", cl.workerID, "reason", req.reason)
		cl.writeReloadFrame(ctx, wsproto.TypeCaps, caps)
	}
}

// doReload runs the injected ReloadFunc (read + build + validate + apply + re-derive
// capabilities). A worker built without one (no config source wired) reports the
// reload as failed rather than pretending it succeeded.
func (cl *Client) doReload() (wsproto.Caps, error) {
	if cl.reloadFn == nil {
		return wsproto.Caps{}, errors.New("worker: config reload is not wired")
	}
	return cl.reloadFn()
}

// writeReloadFrame sends one connection-level reload frame (no job id) under a
// bounded write deadline (reloadWriteTimeout). A write failure is expected and
// benign when the worker is currently disconnected — the new config IS applied
// locally regardless, and the next register re-reports the fresh capabilities.
func (cl *Client) writeReloadFrame(ctx context.Context, t wsproto.FrameType, payload any) {
	wctx, cancel := context.WithTimeout(ctx, reloadWriteTimeout)
	defer cancel()
	if err := cl.writeFrame(wctx, t, "", payload); err != nil {
		slog.Warn("worker could not send reload frame",
			"worker_id", cl.workerID, "frame", string(t), "err", err)
	}
}

// storeCaps replaces the advertised capability snapshot after a successful reload.
func (cl *Client) storeCaps(c wsproto.Caps) {
	cl.capsMu.Lock()
	cl.caps = c
	cl.capsMu.Unlock()
}

// currentCaps returns the capability snapshot to advertise (register frame).
func (cl *Client) currentCaps() wsproto.Caps {
	cl.capsMu.RLock()
	defer cl.capsMu.RUnlock()
	return cl.caps
}
