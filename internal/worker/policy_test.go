package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// recordSeam is a fake ReloadFunc that records every policy it was asked to apply and
// can block / delay / fail on demand. It stands in for the command's real projection
// seam so the worker state-machine tests exercise gen/pending/latest-wins logic
// WITHOUT a real core.
type recordSeam struct {
	mu       sync.Mutex
	got      []int64 // Rev of every non-nil policy applied (in apply order)
	nilCount int     // p == nil calls (LEGACY / no-op SIGHUP re-projections)
	err      error
	// onApply, when set, is called INSIDE the apply (holding applyMu) — a test uses it
	// to block the first apply so it can pile up offers behind it.
	onApply func(p *wsproto.Policy)
}

func (s *recordSeam) fn(p *wsproto.Policy) (ReloadOutcome, error) {
	if s.onApply != nil {
		s.onApply(p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return ReloadOutcome{}, s.err
	}
	if p == nil {
		s.nilCount++
		return ReloadOutcome{Caps: wsproto.Caps{}}, nil
	}
	s.got = append(s.got, p.Rev)
	keys := make([]string, 0, len(p.Projects))
	for _, pp := range p.Projects {
		keys = append(keys, pp.Key)
	}
	sort.Strings(keys)
	return ReloadOutcome{Caps: wsproto.Caps{Projects: keys}, AppliedRev: p.Rev}, nil
}

func (s *recordSeam) applied() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.got...)
}

func (s *recordSeam) nils() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nilCount
}

// pol builds a policy at rev with the given project keys (host_path unused by the fake
// seam; the real projection is tested in the commands package).
func pol(rev int64, keys ...string) wsproto.Policy {
	p := wsproto.Policy{Rev: rev}
	for _, k := range keys {
		p.Projects = append(p.Projects, wsproto.PolicyProject{Key: k, HostPath: "/logical/" + k})
	}
	return p
}

// newPolicyClient stands up a POLICY-mode worker Client wired to seam and dialed to a
// throwaway WS server that drains the frames the worker writes (Applied etc). It does
// NOT start the reload executor — a test starts it (or drives tryApplyPending directly).
func newPolicyClient(t *testing.T, seam ReloadFunc) (*Client, chan wsproto.Envelope) {
	t.Helper()
	frames := make(chan wsproto.Envelope, 64)
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		for {
			var env wsproto.Envelope
			if rerr := wsjson.Read(req.Context(), conn, &env); rerr != nil {
				return
			}
			frames <- env
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	cl := New(Config{WorkerID: "w1", URLs: []string{wsURL}, Token: "t", PolicyMode: true, Reload: seam}, &stubJobs{})
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cl.setConn(conn)
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return cl, frames
}

// waitRev blocks until the worker's session lastRev reaches want (or fails on timeout).
func waitRev(t *testing.T, cl *Client, want int64) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if cl.appliedRev() == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for appliedRev==%d, have %d", want, cl.appliedRev())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestPolicyRevPerConnection (verification 21 / 16): a new session resets the per-session
// Rev high-water mark, so a NEW server that restarts from a low Rev is not permanently
// ignored — its Rev 1..N are applied even though a prior connection reached Rev 100.
func TestPolicyRevPerConnection(t *testing.T) {
	seam := &recordSeam{}
	cl, _ := newPolicyClient(t, seam.fn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	// Connection 1 reaches Rev 100.
	gen1 := cl.beginSession(nil)
	cl.offerPolicy(gen1, pol(100, "a"))
	waitRev(t, cl, 100)

	// Connection 2 (new server, restarts at Rev 1). beginSession resets lastRev to 0, so
	// Rev 1 is strictly newer than the session floor and is applied.
	gen2 := cl.beginSession(nil)
	if gen2 == gen1 {
		t.Fatalf("beginSession must bump the generation, got %d twice", gen2)
	}
	if r := cl.appliedRev(); r != 0 {
		t.Fatalf("beginSession must reset lastRev to 0, got %d", r)
	}
	cl.offerPolicy(gen2, pol(1, "b"))
	waitRev(t, cl, 1)

	got := seam.applied()
	if len(got) != 2 || got[0] != 100 || got[1] != 1 {
		t.Fatalf("applied revs = %v, want [100 1] (new session accepts the low rev)", got)
	}
}

// TestStaleGenerationPolicyDropped (verification 21, session generation + fence): a
// policy taken for gen1 but whose session turned over to gen2 BEFORE it applied must be
// dropped — its config must never reach the seam (core), and gen2's own policy applies.
//
// Falsification: delete the step-3 fence (the `if staleGen { ... return }` block in
// tryApplyPending). Then gen1's Rev 100 reaches the seam despite the turnover, and the
// "Rev 100 must never be applied" assertion below fails.
func TestStaleGenerationPolicyDropped(t *testing.T) {
	seam := &recordSeam{}
	cl, _ := newPolicyClient(t, seam.fn)
	ctx := context.Background()

	gen1 := cl.beginSession(nil)
	cl.offerPolicy(gen1, pol(100, "old")) // gen1 pending Rev=100

	// Model a new handshake landing in the TOCTOU window between taking the pending and
	// acquiring applyMu: turn over to gen2 with its own Rev=5.
	var once sync.Once
	cl.afterTakePendingHook = func() {
		once.Do(func() { cl.beginSession(ptrPolicy(pol(5, "new"))) })
	}

	cl.tryApplyPending(ctx) // takes gen1/Rev100 → hook turns over → fence drops it
	cl.tryApplyPending(ctx) // takes gen2/Rev5 → applies

	got := seam.applied()
	for _, r := range got {
		if r == 100 {
			t.Fatalf("stale gen1 Rev 100 reached the seam: applied=%v (fence failed)", got)
		}
	}
	if len(got) != 1 || got[0] != 5 {
		t.Fatalf("applied revs = %v, want only gen2's [5]", got)
	}
	if cl.currentGen() != gen1+1 {
		t.Fatalf("gen = %d, want %d after one turnover", cl.currentGen(), gen1+1)
	}
	if cl.appliedRev() != 5 {
		t.Fatalf("lastRev = %d, want 5 (gen1's 100 must not pollute gen2)", cl.appliedRev())
	}
}

// TestBeginSessionReachesExecutorUnderBackpressure (verification 21, beginSession 必达):
// beginSession changes the locked state DIRECTLY + wakes — it never rides the bounded
// reloadCh — so a full reload queue + a busy executor cannot make a new handshake's gen
// turnover or its policy get lost.
//
// Falsification: route beginSession's turnover through enqueueReload instead of the
// direct state change; with the queue filled below, the enqueue is dropped, gen never
// switches and the new policy is never applied — waitRev times out.
func TestBeginSessionReachesExecutorUnderBackpressure(t *testing.T) {
	release := make(chan struct{})
	var reloadStarted sync.WaitGroup
	reloadStarted.Add(1)
	var once sync.Once
	seam := &recordSeam{onApply: func(p *wsproto.Policy) {
		if p == nil { // the SIGHUP reloads block here until released
			once.Do(reloadStarted.Done)
			<-release
		}
	}}
	cl, _ := newPolicyClient(t, seam.fn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	// Occupy the executor with a slow SIGHUP reload, then fill the reload queue to the brim.
	if !cl.enqueueReload(reloadReq{reason: "block"}) {
		t.Fatal("first reload must enqueue")
	}
	reloadStarted.Wait() // executor is now stuck inside the first reload
	for i := 0; i < reloadQueueCap; i++ {
		if !cl.enqueueReload(reloadReq{reason: "fill"}) {
			t.Fatalf("reload %d must fit the queue", i)
		}
	}
	if cl.enqueueReload(reloadReq{reason: "overflow"}) {
		t.Fatal("reload queue must be full now")
	}

	// A new handshake arrives while the queue is jammed. gen must switch SYNCHRONOUSLY.
	gen := cl.beginSession(ptrPolicy(pol(7, "p")))
	if cl.currentGen() != gen || gen == 0 {
		t.Fatalf("beginSession must switch gen synchronously regardless of the queue, gen=%d", gen)
	}
	close(release) // let the blocked reloads drain; the policy must still be applied
	waitRev(t, cl, 7)
}

// TestPolicyLatestWinsUnderBackpressure (verification 22): while the executor is busy
// applying, a burst of >reloadQueueCap increasing Revs must converge to the MAX Rev —
// latest-wins keeps only the newest input, never a bounded FIFO that would drop the tail.
//
// Falsification: change offerPolicy to DROP a new offer when a pending already exists
// (a satisfy-then-drop FIFO of depth 1). The burst below then sticks at the first Rev
// instead of the last, and the "converge to 30" assertion fails.
func TestPolicyLatestWinsUnderBackpressure(t *testing.T) {
	firstApply := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	seam := &recordSeam{onApply: func(p *wsproto.Policy) {
		once.Do(func() { close(firstApply); <-release })
	}}
	cl, _ := newPolicyClient(t, seam.fn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	gen := cl.beginSession(nil)
	cl.offerPolicy(gen, pol(1, "p")) // the executor takes this and blocks inside the seam
	<-firstApply

	// Push far more increasing Revs than the reload queue could ever hold; latest-wins
	// collapses them to the newest pending (30).
	for rev := int64(2); rev <= 30; rev++ {
		cl.offerPolicy(gen, pol(rev, "p"))
	}
	close(release)
	waitRev(t, cl, 30)

	got := seam.applied()
	if got[len(got)-1] != 30 {
		t.Fatalf("final applied rev = %d, want 30 (latest-wins under backpressure): %v", got[len(got)-1], got)
	}
	// It must have skipped the intermediate revs (collapsed), not applied all 30.
	if len(got) > 3 {
		t.Fatalf("applied %d revs %v, want a collapsed set (1 then 30), not the whole burst", len(got), got)
	}
}

// TestLostWakeupPolicyStillApplied (verification 21, lost-wakeup): an offer made in the
// window just before the executor parks in select must still be applied. The capacity-1
// policyWake RETAINS the token even though the executor is not yet parked (so the send
// can't rendezvous), and the next select returns to re-read the state.
//
// Falsification: make policyWake unbuffered (cap 0 in New). The offer's non-blocking
// wake in the pre-park window then has no receiver and is dropped → the executor parks on
// an empty channel → Rev 1 is stuck → waitRev times out.
func TestLostWakeupPolicyStillApplied(t *testing.T) {
	seam := &recordSeam{}
	cl, _ := newPolicyClient(t, seam.fn)
	gen := cl.beginSession(nil)
	// Drop the beginSession wake token so the executor would otherwise park on an empty
	// channel — the offer below is the ONLY thing that can wake it.
	select {
	case <-cl.policyWake:
	default:
	}

	offered := make(chan struct{})
	var once sync.Once
	// Fired before the executor parks: it is NOT in select yet, so the offer's wake send
	// cannot rendezvous — only the capacity-1 buffer can carry the token across the park.
	cl.beforeParkHook = func() {
		once.Do(func() {
			cl.offerPolicy(gen, pol(1, "p"))
			close(offered)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	<-offered
	waitRev(t, cl, 1)
}

// TestSighupReprojectsLastKnownGood (verification 9, in-memory): after applying a
// policy, a SIGHUP re-projection must hand the seam the SAME last-known-good policy
// (not nil) — this is the in-memory lastPolicy that a SIGHUP needs when the cache is
// disabled.
//
// Falsification: remove `cl.st.lastPolicy = &lp` from tryApplyPending's commit step;
// the SIGHUP below then calls the seam with p == nil, and the "seam saw Rev 5" assertion
// fails.
func TestSighupReprojectsLastKnownGood(t *testing.T) {
	var mu sync.Mutex
	var seq []*wsproto.Policy // every p the seam was handed, in call order
	gotCall := make(chan struct{}, 8)
	seam := func(p *wsproto.Policy) (ReloadOutcome, error) {
		mu.Lock()
		seq = append(seq, p)
		mu.Unlock()
		select {
		case gotCall <- struct{}{}:
		default:
		}
		if p != nil {
			return ReloadOutcome{Caps: wsproto.Caps{Projects: []string{"p"}}, AppliedRev: p.Rev}, nil
		}
		return ReloadOutcome{}, nil
	}
	cl, _ := newPolicyClient(t, seam)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	gen := cl.beginSession(nil)
	cl.offerPolicy(gen, pol(5, "p")) // apply Rev 5 → sets in-memory last-known-good
	waitRev(t, cl, 5)
	<-gotCall // the apply call

	if !cl.enqueueReload(reloadReq{reason: "sighup"}) {
		t.Fatal("enqueue sighup failed")
	}
	select {
	case <-gotCall:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the SIGHUP re-projection")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seq) < 2 {
		t.Fatalf("seam called %d times, want at least the apply + the SIGHUP", len(seq))
	}
	sighup := seq[len(seq)-1]
	if sighup == nil {
		t.Fatal("SIGHUP re-projected with a nil policy — the in-memory last-known-good was lost")
	}
	if sighup.Rev != 5 {
		t.Fatalf("SIGHUP re-projected Rev %d, want the last-known-good Rev 5", sighup.Rev)
	}
}

// TestV3ServerReconnectKeepsLastKnownGood (verification 16): a POLICY worker that
// reconnects to a server that pushes NO policy (v3, ack.Policy == nil) keeps its
// last-known-good — beginSession(nil) never clears lastPolicy, so a following SIGHUP
// still re-projects the retained policy (projects are not zeroed).
func TestV3ServerReconnectKeepsLastKnownGood(t *testing.T) {
	seam := &recordSeam{}
	cl, _ := newPolicyClient(t, seam.fn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	gen := cl.beginSession(nil)
	cl.offerPolicy(gen, pol(9, "a", "b"))
	waitRev(t, cl, 9)

	// Reconnect to a v3 server: no ack policy. This must NOT clear the last-known-good.
	cl.beginSession(nil)
	if lp := cl.snapshotLastPolicy(); lp == nil || lp.Rev != 9 {
		t.Fatalf("v3 reconnect cleared the last-known-good: %+v", lp)
	}
}

// ptrPolicy returns a pointer to a copy of p (test helper for ack-policy args).
func ptrPolicy(p wsproto.Policy) *wsproto.Policy { return &p }
