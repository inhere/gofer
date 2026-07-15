package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// ReloadOutcome is what the injected reload seam reports after building + applying a
// config: the capability snapshot of the config it applied (Caps — the SAME source a
// later reconnect registers with), plus the policy-apply diagnostics the Applied frame
// carries. AppliedRev echoes the projected Policy's Rev (0 for a legacy/no-op reload);
// Rejected/Degraded are diagnostic only and never gate routing (wsproto.Applied).
type ReloadOutcome struct {
	Caps       wsproto.Caps
	AppliedRev int64
	Rejected   []wsproto.AppliedRejection
	Degraded   []wsproto.AppliedDegrade
}

// ReloadFunc re-reads the worker's config from disk and applies it, returning the
// capability snapshot derived from the SAME config it applied.
//
// It is INJECTED by the command layer (only it knows where worker.yaml lives and
// how to map it onto a config.Config); the worker package owns the orchestration
// around it (queueing, serialisation, session generations, receipts) — see G021.
//
// The p argument is the policy to project onto the local config:
//   - p != nil (POLICY mode apply, or a SIGHUP re-projecting the in-memory
//     last-known-good): the seam projects p's projects through the worker's roots.
//   - p == nil (LEGACY SIGHUP, or a POLICY SIGHUP with no last-known-good yet): the
//     seam sources projects from its own local config (LEGACY) or no-ops the project
//     set (POLICY, T5-A) — it must NEVER wipe a running POLICY config to empty.
//
// Contract the implementation MUST honour (this is what makes "a bad config keeps
// the old one" true): build and validate the new config COMPLETELY first, and only
// then apply it (core.ReloadWith). Any failure — unreadable file, bad YAML, failed
// validation — must return an error BEFORE anything is applied, leaving the worker
// running its previous config untouched. The apply stage cannot fail, so it cannot
// roll back. The returned Caps must be derived from the very config that was applied.
type ReloadFunc func(p *wsproto.Policy) (ReloadOutcome, error)

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
	// caps / applied). The reload executor is a SINGLE goroutine, so a write onto a
	// half-open socket must not park it forever — that would stall every later reload
	// behind it. It is well under DefaultReadDeadline (45s), which stays the
	// authoritative disconnect detector: a timed-out write just abandons this receipt,
	// and the recv loop tears the session down on its own schedule.
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

// reloadLoop is the worker's SERIAL config executor: one goroutine consuming one
// select, so reloads and policy applies are applied strictly one at a time (a stale
// config can never win a race) and only one config is ever being applied. It is
// started once per Client by Run and lives across reconnects; ctx is the worker's
// lifetime ctx.
//
// It has THREE wake sources (M1: one goroutine, not three):
//   - ctx.Done   → exit;
//   - reloadCh   → a SIGHUP / remote reload (runReloadReq, receipt = caps/reload_result);
//   - policyWake → a pushed Policy is waiting to be applied (tryApplyPending, T5-C).
//
// Limited fairness (no livelock): each turn processes at most ONE reload or ONE
// tryApplyPending, so a stream of policies cannot starve reloads and vice-versa.
func (cl *Client) reloadLoop(ctx context.Context) {
	for {
		// Test-only seam fired right BEFORE the executor parks in select, to model an
		// offer landing in the pre-park window (the B2 lost-wakeup case). nil in prod.
		if cl.beforeParkHook != nil {
			cl.beforeParkHook()
		}
		select {
		case <-ctx.Done():
			return
		case req := <-cl.reloadCh:
			cl.runReloadReq(ctx, req)
		case <-cl.policyWake:
			cl.tryApplyPending(ctx)
		}
	}
}

// runReloadReq executes exactly one queued SIGHUP / remote reload (always on the
// executor goroutine). It re-projects the in-memory last-known-good policy (POLICY)
// or re-reads the local projects (LEGACY) through the SAME seam a policy apply uses
// (runReload) — the seam decides based on the freshly-read mode + the passed policy.
//
// A remote request (requestID != "") is answered with a reload_result carrying the
// outcome — including the failure, so the caller learns the config was refused
// instead of being told "accepted". A local SIGHUP has nobody to answer, so a
// successful one re-reports the new capabilities with an unsolicited caps frame; a
// failed one is logged (the old config keeps running, the worker does not exit).
func (cl *Client) runReloadReq(ctx context.Context, req reloadReq) {
	// The last-known-good policy is what a POLICY-mode SIGHUP re-projects with the new
	// yaml; nil (LEGACY, or POLICY before any Policy arrived) lets the seam source
	// projects locally / no-op the project set (T5-A). It is NOT applied as a new Rev
	// (SIGHUP does not advance the session's Rev), so lastRev/lastPolicy stay put.
	out, err := cl.runReload(cl.snapshotLastPolicy())
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
			c := out.Caps
			res.Caps = &c
		}
		cl.writeReloadFrame(ctx, wsproto.TypeReloadResult, res)
	case err != nil:
		slog.Error("worker reload failed, keeping old config",
			"worker_id", cl.workerID, "reason", req.reason, "err", err)
	default:
		slog.Info("worker config reloaded", "worker_id", cl.workerID, "reason", req.reason)
		cl.writeReloadFrame(ctx, wsproto.TypeCaps, out.Caps)
	}
}

// runReload is the SINGLE execution body (T5-C): it applies policy p (or re-projects
// locally when p is nil) through the injected seam and, on success, REPLACES the
// advertised capability snapshot (storeCaps) so a later reconnect registers with the
// config just applied — never a second apply path that would register stale caps.
//
// It MUST NOT acquire applyMu itself: tryApplyPending already holds it, and the
// reloadCh path is single-goroutine w.r.t. apply, so re-taking it here would
// self-deadlock (tryApplyPending → runReload → applyMu).
func (cl *Client) runReload(p *wsproto.Policy) (ReloadOutcome, error) {
	if cl.reloadFn == nil {
		return ReloadOutcome{}, errors.New("worker: config reload is not wired")
	}
	out, err := cl.reloadFn(p)
	if err == nil {
		cl.storeCaps(out.Caps)
	}
	return out, err
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
