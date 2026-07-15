package worker

import (
	"context"
	"log/slog"
	"sync"

	"github.com/inhere/gofer/internal/wsproto"
)

// pendingPolicy is one latest-wins policy input waiting to be applied, tagged with
// the session generation it belongs to (H1/B1). It is an immutable value: once set
// on sessionState.pending it is never mutated in place, only replaced.
type pendingPolicy struct {
	gen    uint64
	policy wsproto.Policy
}

// sessionState is the SINGLE source of truth for the worker's policy session
// (v0.4 D-B1/B2 + E-B3). gen/lastRev/pending/lastPolicy are all mutated only under
// mu; the handshake goroutine (beginSession) and the recv loop (offerPolicy) and the
// executor (tryApplyPending) all funnel through it.
type sessionState struct {
	mu      sync.Mutex
	gen     uint64 // current session generation (+1 on every successful handshake)
	lastRev int64  // highest Rev applied THIS session (reset to 0 on beginSession)
	// pending is the latest-wins input for the current gen still to be applied.
	pending *pendingPolicy
	// lastPolicy is the most recent successfully-applied full snapshot — the local
	// last-known-good (LKG). It is kept ACROSS gens (H3: beginSession never clears it)
	// so a SIGHUP re-projects it and a v3-server reconnect keeps the projects. Distinct
	// from lastRev (session-scoped) — do not collapse the two into one field.
	lastPolicy *wsproto.Policy
}

// wake sends a non-blocking token onto a capacity-1 channel: if a token is already
// buffered (the executor has not consumed the last wake) it is dropped — the merged
// token still means "there may be work", which the executor re-checks under st.mu.
// This is the B2 lost-wakeup guard: the token survives in the channel even when the
// executor is not parked, so an offer made in the tiny window before the executor
// re-parks is never lost.
func wake(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// wakeExecutor signals the config executor that policy work may be available.
func (cl *Client) wakeExecutor() { wake(cl.policyWake) }

// beginSession is the ONLY entry for a fresh policy session (called from runSession
// on a successful handshake). It atomically bumps the generation, resets the
// per-session Rev high-water mark, drops any stale-gen pending, and — when the ack
// carried a Policy — seeds it as this gen's first pending input. It NEVER touches
// lastPolicy (H3: the LKG spans reconnects). It returns the new generation so the
// caller threads it into offerPolicy for frames received on this connection.
//
// It takes applyMu FIRST (lock order applyMu → st.mu, B1) so it can never interleave
// with the executor's fence+apply+commit: at worst it waits one in-flight apply
// (~2s), which is fine on the (cold) handshake path.
func (cl *Client) beginSession(ackPolicy *wsproto.Policy) uint64 {
	cl.applyMu.Lock()
	cl.st.mu.Lock()
	cl.st.gen++
	gen := cl.st.gen
	cl.st.lastRev = 0
	cl.st.pending = nil
	if ackPolicy != nil {
		p := *ackPolicy
		cl.st.pending = &pendingPolicy{gen: gen, policy: p}
	}
	// lastPolicy deliberately NOT cleared (H3).
	cl.st.mu.Unlock()
	cl.applyMu.Unlock()
	cl.wakeExecutor()
	return gen
}

// offerPolicy records a policy received on the connection whose generation is gen
// (an ack-bundled catch-up push or a mid-session TypePolicy frame). It only holds
// st.mu (never applyMu), so a recv loop is never blocked behind an in-flight apply.
// It sets pending only when the frame belongs to the CURRENT gen and its Rev is
// strictly above the floor (max of lastRev and any same-gen pending Rev, H1), then
// wakes the executor OUTSIDE the lock (contract with tryApplyPending).
func (cl *Client) offerPolicy(gen uint64, p wsproto.Policy) {
	cl.st.mu.Lock()
	floor := cl.st.lastRev
	if cl.st.pending != nil && cl.st.pending.gen == gen && cl.st.pending.policy.Rev > floor {
		floor = cl.st.pending.policy.Rev
	}
	ok := gen == cl.st.gen && p.Rev > floor
	if ok {
		cl.st.pending = &pendingPolicy{gen: gen, policy: p}
	}
	cl.st.mu.Unlock()
	if ok {
		cl.wakeExecutor()
	}
}

// snapshotLastPolicy returns a copy of the last-known-good policy (nil if none). The
// copy means the caller can hand the pointer to the seam without exposing st's field.
func (cl *Client) snapshotLastPolicy() *wsproto.Policy {
	cl.st.mu.Lock()
	defer cl.st.mu.Unlock()
	if cl.st.lastPolicy == nil {
		return nil
	}
	p := *cl.st.lastPolicy
	return &p
}

// tryApplyPending is the executor's policy step (T5-C, six stages). It applies the
// current gen's pending policy exactly once, fenced so a session that turned over
// mid-apply can never publish a stale generation's config to the core.
func (cl *Client) tryApplyPending(ctx context.Context) {
	// 1. Take the pending input under st.mu and re-validate it (H1: verify AFTER take).
	cl.st.mu.Lock()
	p := cl.st.pending
	if p == nil || p.gen != cl.st.gen || p.policy.Rev <= cl.st.lastRev {
		cl.st.mu.Unlock()
		return
	}
	localGen, localPol := p.gen, p.policy
	cl.st.pending = nil
	cl.st.mu.Unlock()

	// Test seam: model a beginSession racing in the TOCTOU window between taking the
	// pending and acquiring applyMu (F-B1). nil in production.
	if cl.afterTakePendingHook != nil {
		cl.afterTakePendingHook()
	}

	// 2. Acquire applyMu (B1). May wait for an in-flight beginSession to release it.
	cl.applyMu.Lock()

	// 3. Fence: a beginSession between step 1 and step 2 would have bumped gen. If so,
	//    this pending belongs to a dead session — drop it BEFORE applying (so its
	//    config never reaches the core).
	cl.st.mu.Lock()
	staleGen := localGen != cl.st.gen
	cl.st.mu.Unlock()
	if staleGen {
		cl.applyMu.Unlock()
		return
	}

	// 4. Apply (projection + agent.Resolve + detect + ReloadWith + storeCaps). Slow
	//    (~2s); the whole segment holds applyMu so beginSession cannot slip in.
	out, err := cl.runReload(&localPol)

	// 5. Commit under st.mu, but only if the session did NOT turn over during apply.
	//    Publishing the apply seq happens here, still under applyMu (H4).
	committed := false
	var seq uint64
	cl.st.mu.Lock()
	if err == nil && localGen == cl.st.gen {
		if localPol.Rev > cl.st.lastRev {
			cl.st.lastRev = localPol.Rev
		}
		lp := localPol
		cl.st.lastPolicy = &lp // H3: only a successful apply advances the LKG
		seq = cl.applySeq.Add(1)
		committed = true
	}
	cl.st.mu.Unlock()

	// 6. Release applyMu, then do the off-lock work (cache + Applied receipt).
	cl.applyMu.Unlock()

	if err != nil {
		slog.Error("worker policy apply failed, keeping old config",
			"worker_id", cl.workerID, "rev", localPol.Rev, "err", err)
		cl.wakeExecutor() // a newer offer may have arrived; re-check
		return
	}
	if !committed {
		// Session turned over between take and commit: this apply is void. A newer
		// beginSession/offer drives the live gen forward.
		cl.wakeExecutor()
		return
	}

	// Persist the last-known-good BEFORE announcing (T5-F): the Applied frame carries
	// policy_cache_stale only if the cache could not be written, so it must be decided
	// first and can never be contradicted afterwards.
	degraded := out.Degraded
	if cacheErr := cl.writePolicyCacheGuarded(&localPol, seq); cacheErr != nil {
		degraded = append(append([]wsproto.AppliedDegrade(nil), degraded...),
			wsproto.AppliedDegrade{Key: "*", Gate: gateCacheStale})
		cl.invalidatePolicyCache() // do not let a restart replay an over-Rev stale cache
		cl.enqueueCacheRetry(&localPol, seq)
	}
	caps := out.Caps
	cl.writeAppliedFrame(ctx, wsproto.Applied{
		Rev:      localPol.Rev,
		Caps:     &caps,
		Rejected: out.Rejected,
		Degraded: degraded,
	})
	cl.wakeExecutor() // self-continue: an offer during apply left a fresh pending
}

// replyLegacyApplied answers a pushed Policy that a non-POLICY (LEGACY/EMPTY) worker
// will NOT apply: it echoes the Rev with the worker's LOCAL caps and a
// legacy_local_projects degrade, so the hub clears its pending Rev and the Cluster
// page shows the worker is fine — it just sources projects locally (verification 1).
func (cl *Client) replyLegacyApplied(ctx context.Context, rev int64) {
	caps := cl.currentCaps()
	cl.writeAppliedFrame(ctx, wsproto.Applied{
		Rev:      rev,
		Caps:     &caps,
		Degraded: []wsproto.AppliedDegrade{{Key: "*", Gate: gateLegacyLocalProjects}},
	})
}

// writeAppliedFrame sends one applied frame (no job id) under the bounded reload
// write deadline. Like a caps frame, a write failure while disconnected is benign —
// the config IS applied locally and the next register re-reports it.
func (cl *Client) writeAppliedFrame(ctx context.Context, a wsproto.Applied) {
	wctx, cancel := context.WithTimeout(ctx, reloadWriteTimeout)
	defer cancel()
	if err := cl.writeFrame(wctx, wsproto.TypeApplied, "", a); err != nil {
		slog.Warn("worker could not send applied frame",
			"worker_id", cl.workerID, "rev", a.Rev, "err", err)
	}
}

// appliedRev / currentGen are locked read accessors used by tests to observe the
// session state without reaching into st directly.
func (cl *Client) appliedRev() int64 {
	cl.st.mu.Lock()
	defer cl.st.mu.Unlock()
	return cl.st.lastRev
}

func (cl *Client) currentGen() uint64 {
	cl.st.mu.Lock()
	defer cl.st.mu.Unlock()
	return cl.st.gen
}
