package wshub

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// fixedPolicySource returns the same Policy for every worker/every call (rev never
// advances). It stands in for corePolicySource in hub-only tests.
type fixedPolicySource struct{ pol wsproto.Policy }

func (f fixedPolicySource) PolicyFor(string) (wsproto.Policy, bool) { return f.pol, true }

// flipPolicySource returns rev=base on its FIRST PolicyFor call and rev=base+1 on every
// call after. It models the §7-N1 window precisely: the ack is computed off generation N,
// then the config advances by one generation (a reload / PushPolicyAll that could not see
// the not-yet-registered conn), and the catch-up read now observes N+1. No production hook
// is needed — the two PolicyFor calls Accept makes (ack, then catch-up) drive the flip.
type flipPolicySource struct {
	mu    sync.Mutex
	calls int
	base  int64
}

func (f *flipPolicySource) PolicyFor(string) (wsproto.Policy, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return wsproto.Policy{Rev: f.base}, true
	}
	return wsproto.Policy{Rev: f.base + 1}, true
}

// readNextPolicy reads frames off conn (skipping control ping/pong) until it sees a
// policy frame, failing if the (bounded) context expires first. The bounded ctx is what
// makes the catch-up falsification a real FAILURE rather than a hang: with catch-up
// removed no second policy frame ever arrives and this fails on the deadline.
func readNextPolicy(t *testing.T, ctx context.Context, conn *websocket.Conn) wsproto.Policy {
	t.Helper()
	for {
		env, err := readEnvelope(ctx, conn)
		if err != nil {
			t.Fatalf("waiting for a policy frame: %v", err)
		}
		switch env.Type {
		case wsproto.TypePolicy:
			p, derr := wsproto.As[wsproto.Policy](env)
			if derr != nil {
				t.Fatalf("decode policy frame: %v", derr)
			}
			return p
		case wsproto.TypePing, wsproto.TypePong:
			continue
		default:
			t.Fatalf("unexpected frame %q while waiting for a policy frame", env.Type)
		}
	}
}

// TestRegisterCatchUpPolicy is validation 7 (§7-N1). The ack is computed off rev=N; a
// reload advances the source to N+1 in the window before Put makes the conn visible (the
// broadcast for N+1 could not reach a conn that was not yet registered). The catch-up push
// after Put must re-read the source and deliver N+1, so the worker converges to N+1.
//
// Falsification (proven manually): delete the h.catchUpPolicy call in Accept — PolicyFor is
// then called only once (the ack, rev=N), no second frame is ever written, and this test
// fails on readNextPolicy's bounded deadline: the worker is stuck on N forever.
func TestRegisterCatchUpPolicy(t *testing.T) {
	const base = 5
	hub := New(map[string]string{"w1": "w1"})
	hub.SetPolicySource(&flipPolicySource{base: base})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	// The ack carries the rev the source returned on the FIRST read (N).
	if reg.Policy == nil || reg.Policy.Rev != base {
		t.Fatalf("ack policy = %+v, want a bundled policy at rev %d", reg.Policy, base)
	}

	// The catch-up push after Put must deliver N+1 — a bounded read so the falsification
	// (no catch-up ⇒ no frame) fails instead of hanging.
	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readCancel()
	if got := readNextPolicy(t, readCtx, conn); got.Rev != base+1 {
		t.Fatalf("catch-up policy rev = %d, want %d (worker never converged past the ack rev)", got.Rev, base+1)
	}

	// Server-side pending reflects the highest rev pushed (the catch-up's N+1, mark-after-write).
	waitFor(t, func() bool {
		snap, ok := hub.WorkerSnapshot("w1")
		return ok && snap.PolicyPending && snap.PolicyRev == base+1
	})
}

// TestLateAppliedCannotReplaceNewConnection is the "old process poisons the new one"
// regression for policy state (mirror of TestUpdateCapsFromSupersededConnIgnored). A worker
// restarts: the registry points at conn B (pending at rev 8), but conn A's read loop is
// still draining and delivers a late Applied. MarkPolicyApplied must drop it — keying by
// worker_id instead would let the dead process clear B's pending and report an un-converged
// worker as converged.
func TestLateAppliedCannotReplaceNewConnection(t *testing.T) {
	r := newRegistry()
	base := wsproto.Register{WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion}

	connA := newWorkerConn("w1", "w1", nil, base)
	r.Put(connA)

	connB := newWorkerConn("w1", "w1", nil, base) // restart: B replaces A as the current conn
	r.Put(connB)
	connB.markPolicyPending(8) // hub pushed rev 8 to the live conn

	// A late Applied from the SUPERSEDED conn A (even at the same/greater rev) must not
	// clear B's pending.
	r.MarkPolicyApplied(connA, 8, nil, nil)

	snap, ok := r.WorkerSnapshot("w1")
	if !ok {
		t.Fatal("worker vanished from the registry")
	}
	if !snap.PolicyPending || snap.PolicyRev != 8 || snap.AppliedRev != 0 {
		t.Fatalf("a superseded conn's late Applied leaked into the live one: %+v", snap)
	}

	// The live conn's own Applied still clears it.
	r.MarkPolicyApplied(connB, 8, nil, nil)
	if snap, _ := r.WorkerSnapshot("w1"); snap.PolicyPending || snap.AppliedRev != 8 {
		t.Fatalf("live conn's Applied did not clear pending: %+v", snap)
	}
}

// TestMarkPolicyPendingMaxAndStaleAppliedMonotonic pins the two Rev-monotonic rules
// (F-HIGH-2 / E-HIGH-1): markPolicyPending only ever RAISES the pushed rev, and
// MarkPolicyApplied clears pending only for a rev >= the pushed one — a stale Applied for
// an older rev is ignored and never rolls the state back or resurrects "converged".
func TestMarkPolicyPendingMaxAndStaleAppliedMonotonic(t *testing.T) {
	r := newRegistry()
	wc := newWorkerConn("w1", "w1", nil, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
	})
	r.Put(wc)

	wc.markPolicyPending(6)
	wc.markPolicyPending(4) // lower: must NOT lower the pushed rev
	if snap, _ := r.WorkerSnapshot("w1"); snap.PolicyRev != 6 || !snap.PolicyPending {
		t.Fatalf("markPolicyPending not max-monotonic: %+v", snap)
	}

	// Stale Applied (rev 5 < pushed 6) is ignored: pending stays, appliedRev stays 0.
	r.MarkPolicyApplied(wc, 5, nil, nil)
	if snap, _ := r.WorkerSnapshot("w1"); !snap.PolicyPending || snap.AppliedRev != 0 {
		t.Fatalf("stale Applied(5) wrongly cleared a pending for rev 6: %+v", snap)
	}

	// Applied at the pushed rev clears it.
	r.MarkPolicyApplied(wc, 6, nil, nil)
	if snap, _ := r.WorkerSnapshot("w1"); snap.PolicyPending || snap.AppliedRev != 6 {
		t.Fatalf("Applied(6) did not clear pending / record appliedRev: %+v", snap)
	}

	// A newer push re-pends; an Applied AHEAD of it still clears (>=).
	wc.markPolicyPending(7)
	r.MarkPolicyApplied(wc, 9, nil, nil)
	if snap, _ := r.WorkerSnapshot("w1"); snap.PolicyPending || snap.AppliedRev != 9 {
		t.Fatalf("Applied(9) ahead of pushed 7 must clear pending: %+v", snap)
	}
}

// TestAckCarriesPolicyAndAppliedClearsPending is validation T4-A/C/D end to end over a fake
// worker: the ack bundles the policy and marks pending; the worker's Applied at that rev
// clears pending and records the applied rev (and routes its Caps through UpdateCaps).
func TestAckCarriesPolicyAndAppliedClearsPending(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	hub.SetPolicySource(fixedPolicySource{pol: wsproto.Policy{Rev: 7}})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	if reg.Policy == nil || reg.Policy.Rev != 7 {
		t.Fatalf("ack policy = %+v, want rev 7 bundled on the ack", reg.Policy)
	}
	// No catch-up frame: the source rev (7) equals the acked rev, so nothing extra is pushed.
	waitFor(t, func() bool {
		snap, ok := hub.WorkerSnapshot("w1")
		return ok && snap.PolicyPending && snap.PolicyRev == 7 && snap.AppliedRev == 0
	})

	// The worker reports it applied rev 7, re-reporting its caps in the same frame.
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeApplied,
		Payload: mustRaw(wsproto.Applied{
			Rev:  7,
			Caps: &wsproto.Caps{Projects: []string{"alpha"}, Agents: []string{"exec"}},
		}),
	}); err != nil {
		t.Fatalf("write applied: %v", err)
	}

	waitFor(t, func() bool {
		snap, ok := hub.WorkerSnapshot("w1")
		return ok && !snap.PolicyPending && snap.AppliedRev == 7 && len(snap.Projects) == 1 && snap.Projects[0] == "alpha"
	})
}

// TestV3WorkerNeverPending is the regression for "v3 worker must not be marked
// policy_pending". A pre-policy worker registers under a wired source: the ack carries no
// policy, no catch-up frame is sent, PushPolicyAll skips it, and its snapshot never goes
// pending. It only misses the frame its version predates — it is never evicted or stalled.
func TestV3WorkerNeverPending(t *testing.T) {
	if wsproto.PolicyMinProtocolVersion-1 < wsproto.MinProtocolVersion {
		t.Skip("no below-policy, at-or-above-floor version to test")
	}
	v3 := wsproto.PolicyMinProtocolVersion - 1
	hub := New(map[string]string{"w1": "w1"})
	hub.SetPolicySource(fixedPolicySource{pol: wsproto.Policy{Rev: 7}})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegisterProto(t, ctx, wsURL, "w1", v3)
	if !reg.Accepted {
		t.Fatalf("v%d worker rejected: %+v", v3, reg)
	}
	if reg.Policy != nil {
		t.Fatalf("v%d worker's ack must NOT bundle a policy, got %+v", v3, reg.Policy)
	}
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	// A broadcast must skip it too (SupportsPolicy gate), so it never goes pending.
	hub.PushPolicyAll()
	if snap, ok := hub.WorkerSnapshot("w1"); !ok || snap.PolicyPending || snap.PolicyRev != 0 {
		t.Fatalf("v%d worker must never be marked pending: ok=%v snap=%+v", v3, ok, snap)
	}

	// And no policy frame is ever delivered to it (only heartbeat control frames may arrive).
	readCtx, readCancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer readCancel()
	for {
		env, err := readEnvelope(readCtx, conn)
		if err != nil {
			break // deadline: no policy frame leaked to the v3 worker (good)
		}
		if env.Type == wsproto.TypePolicy {
			t.Fatalf("a policy frame leaked to a v%d worker", v3)
		}
	}
}

// TestAppliedConcurrentWithSnapshot is the -race proof: MarkPolicyApplied and
// markPolicyPending write the same fields WorkerSnapshot reads, all under wc.mu. Many
// goroutines hammer one live conn at once; -race must stay clean and the count monotonic.
func TestAppliedConcurrentWithSnapshot(t *testing.T) {
	r := newRegistry()
	wc := newWorkerConn("w1", "w1", nil, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
	})
	r.Put(wc)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ { // pushers: mark pending at rising revs
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for rev := int64(1); ; rev++ {
				select {
				case <-stop:
					return
				default:
				}
				wc.markPolicyPending(rev)
			}
		}(i)
	}
	for i := 0; i < 4; i++ { // appliers: report applied at rising revs
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rev := int64(1); ; rev++ {
				select {
				case <-stop:
					return
				default:
				}
				r.MarkPolicyApplied(wc, rev, []wsproto.AppliedRejection{{Key: fmt.Sprintf("k%d", rev)}}, nil)
			}
		}()
	}
	for i := 0; i < 4; i++ { // readers: the /v1/meta + admission surface
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap, ok := r.WorkerSnapshot("w1")
				if ok {
					_ = snap.PolicyPending
					_ = snap.PolicyRev
					_ = snap.AppliedRev
				}
			}
		}()
	}

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}
