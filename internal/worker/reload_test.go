package worker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// capsWith builds a distinguishable caps snapshot (the label is the marker a test
// asserts on).
func capsWith(label string, maxConc int) wsproto.Caps {
	return wsproto.Caps{Labels: []string{label}, MaxConc: maxConc}
}

// waitFrame reads captured frames until one of type t arrives (failing on timeout).
// Frames of other types are skipped, so a test can assert on the frame it cares
// about without ordering assumptions against unrelated traffic.
func waitFrame(t *testing.T, frames chan wsproto.Envelope, ft wsproto.FrameType) wsproto.Envelope {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case env := <-frames:
			if env.Type == ft {
				return env
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %q frame", ft)
		}
	}
}

// TestReloadExecutorAppliesInArrivalOrder (acceptance 7): two reload requests that
// overlap must be applied SERIALLY in arrival order — the second one (fast) may not
// overtake the first (slow) and the LAST request to arrive must be the one whose
// config finally sticks. A per-request goroutine would fail this: B would finish
// first and A's older config would then overwrite it.
func TestReloadExecutorAppliesInArrivalOrder(t *testing.T) {
	jobs := &stubJobs{}
	cl, frames, _ := dialLiveClient(t, jobs)

	var mu sync.Mutex
	var applied []string
	calls := 0
	cl.reloadFn = func() (wsproto.Caps, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 { // A is slow; B (fast) must still wait behind it
			time.Sleep(300 * time.Millisecond)
		}
		label := "A"
		if n == 2 {
			label = "B"
		}
		mu.Lock()
		applied = append(applied, label)
		mu.Unlock()
		return capsWith(label, n), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	cl.onReload(ctx, wsproto.Reload{RequestID: "req-a", Reason: "a"})
	cl.onReload(ctx, wsproto.Reload{RequestID: "req-b", Reason: "b"})

	first := waitFrame(t, frames, wsproto.TypeReloadResult)
	second := waitFrame(t, frames, wsproto.TypeReloadResult)
	r1, err := wsproto.As[wsproto.ReloadResult](first)
	if err != nil {
		t.Fatalf("decode first reload_result: %v", err)
	}
	r2, err := wsproto.As[wsproto.ReloadResult](second)
	if err != nil {
		t.Fatalf("decode second reload_result: %v", err)
	}
	if r1.RequestID != "req-a" || r2.RequestID != "req-b" {
		t.Fatalf("receipts out of order: %q then %q, want req-a then req-b", r1.RequestID, r2.RequestID)
	}
	if !r1.OK || !r2.OK {
		t.Fatalf("both reloads must succeed: %+v %+v", r1, r2)
	}

	mu.Lock()
	got := strings.Join(applied, ",")
	mu.Unlock()
	if got != "A,B" {
		t.Fatalf("apply order = %q, want %q (the executor must be serial)", got, "A,B")
	}
	// The LAST request wins: the advertised snapshot is B's, not A's.
	if caps := cl.currentCaps(); len(caps.Labels) != 1 || caps.Labels[0] != "B" || caps.MaxConc != 2 {
		t.Fatalf("current caps = %+v, want B's snapshot (the last reload applied)", caps)
	}
	if r2.Caps == nil || len(r2.Caps.Labels) != 1 || r2.Caps.Labels[0] != "B" {
		t.Fatalf("last receipt caps = %+v, want B's snapshot", r2.Caps)
	}
}

// TestReloadBadConfigKeepsOldCapsAndReplies (acceptance 4): a reload that fails to
// build the new config must leave the worker running (and advertising) the OLD one
// and must still ANSWER the request with ok=false + the reason — never a silent
// drop, never a fake "accepted". The worker keeps serving afterwards.
func TestReloadBadConfigKeepsOldCapsAndReplies(t *testing.T) {
	jobs := &stubJobs{}
	cl, frames, _ := dialLiveClient(t, jobs)
	cl.storeCaps(capsWith("old", 3))

	fail := true
	var mu sync.Mutex
	cl.reloadFn = func() (wsproto.Caps, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return wsproto.Caps{}, errBadConfig
		}
		return capsWith("new", 5), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	cl.onReload(ctx, wsproto.Reload{RequestID: "req-bad"})
	res, err := wsproto.As[wsproto.ReloadResult](waitFrame(t, frames, wsproto.TypeReloadResult))
	if err != nil {
		t.Fatalf("decode reload_result: %v", err)
	}
	if res.RequestID != "req-bad" || res.OK {
		t.Fatalf("failed reload must reply ok=false for its request: %+v", res)
	}
	if !strings.Contains(res.Err, "decode worker config") {
		t.Fatalf("receipt must carry the reason, got err=%q", res.Err)
	}
	if res.Caps != nil {
		t.Fatalf("a failed reload must not report caps: %+v", res.Caps)
	}
	// Old config untouched: the advertised snapshot is unchanged.
	if caps := cl.currentCaps(); len(caps.Labels) != 1 || caps.Labels[0] != "old" || caps.MaxConc != 3 {
		t.Fatalf("current caps = %+v, want the OLD snapshot kept", caps)
	}

	// The worker is still alive: a subsequent good reload goes through.
	mu.Lock()
	fail = false
	mu.Unlock()
	cl.onReload(ctx, wsproto.Reload{RequestID: "req-good"})
	res2, err := wsproto.As[wsproto.ReloadResult](waitFrame(t, frames, wsproto.TypeReloadResult))
	if err != nil {
		t.Fatalf("decode second reload_result: %v", err)
	}
	if !res2.OK || res2.RequestID != "req-good" {
		t.Fatalf("worker must keep serving reloads after a bad config: %+v", res2)
	}
	if caps := cl.currentCaps(); caps.Labels[0] != "new" {
		t.Fatalf("current caps = %+v, want the new snapshot after the good reload", caps)
	}
}

// errBadConfig stands in for what the injected ReloadFunc returns when worker.yaml
// cannot be read/decoded (the command's loadWorkerConfig error).
var errBadConfig = &reloadTestErr{"decode worker config /etc/worker.yaml: yaml: line 3: mapping values are not allowed"}

type reloadTestErr struct{ msg string }

func (e *reloadTestErr) Error() string { return e.msg }

// TestReloadSighupBroadcastsCaps: a local SIGHUP-triggered reload has no requester,
// so it re-reports the new capabilities with an unsolicited caps frame and must NOT
// emit a reload_result (which would look like the answer to somebody's pending
// request).
func TestReloadSighupBroadcastsCaps(t *testing.T) {
	jobs := &stubJobs{}
	cl, frames, _ := dialLiveClient(t, jobs)
	cl.reloadFn = func() (wsproto.Caps, error) { return capsWith("hup", 7), nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	if !cl.enqueueReload(reloadReq{reason: "sighup"}) { // requestID == "" → local
		t.Fatalf("enqueue sighup reload failed")
	}

	env := waitFrame(t, frames, wsproto.TypeCaps)
	c, err := wsproto.As[wsproto.Caps](env)
	if err != nil {
		t.Fatalf("decode caps: %v", err)
	}
	if len(c.Labels) != 1 || c.Labels[0] != "hup" || c.MaxConc != 7 {
		t.Fatalf("caps frame = %+v, want the reloaded snapshot", c)
	}
	if env.JobID != "" {
		t.Fatalf("caps is a connection-level frame, got job_id=%q", env.JobID)
	}
	if caps := cl.currentCaps(); caps.Labels[0] != "hup" {
		t.Fatalf("sighup reload must update the advertised snapshot, got %+v", caps)
	}
	// No receipt: nobody asked.
	select {
	case env := <-frames:
		if env.Type == wsproto.TypeReloadResult {
			t.Fatalf("a SIGHUP reload must not send a reload_result")
		}
	case <-time.After(200 * time.Millisecond):
	}
}

// TestReloadQueueFullRepliesBusy: the queue is bounded, so an extreme burst of
// reload requests is REJECTED rather than buffered without limit — and the rejected
// request still gets a receipt (ok=false, busy), never a silent drop that would hang
// the caller until its timeout.
func TestReloadQueueFullRepliesBusy(t *testing.T) {
	jobs := &stubJobs{}
	cl, frames, _ := dialLiveClient(t, jobs)
	cl.reloadFn = func() (wsproto.Caps, error) { return capsWith("x", 1), nil }

	// No executor running → nothing drains the queue; fill it to the brim.
	for i := 0; i < reloadQueueCap; i++ {
		if !cl.enqueueReload(reloadReq{requestID: "queued"}) {
			t.Fatalf("enqueue %d must fit in a queue of %d", i, reloadQueueCap)
		}
	}
	if cl.enqueueReload(reloadReq{requestID: "overflow"}) {
		t.Fatalf("the reload queue must be bounded at %d", reloadQueueCap)
	}

	cl.onReload(context.Background(), wsproto.Reload{RequestID: "req-busy"})
	res, err := wsproto.As[wsproto.ReloadResult](waitFrame(t, frames, wsproto.TypeReloadResult))
	if err != nil {
		t.Fatalf("decode reload_result: %v", err)
	}
	if res.RequestID != "req-busy" || res.OK {
		t.Fatalf("an overflowing request must be answered ok=false: %+v", res)
	}
	if !strings.Contains(res.Err, "busy") {
		t.Fatalf("busy receipt err = %q, want it to say the worker is busy", res.Err)
	}
	if res.Caps != nil {
		t.Fatalf("a rejected reload must not report caps")
	}
}

// TestReloadNotWiredIsReportedAsFailure: a worker built without a ReloadFunc cannot
// reload; the request must fail loudly instead of being acked as applied.
func TestReloadNotWiredIsReportedAsFailure(t *testing.T) {
	jobs := &stubJobs{}
	cl, frames, _ := dialLiveClient(t, jobs) // New() without Config.Reload

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	cl.onReload(ctx, wsproto.Reload{RequestID: "req-nowire"})
	res, err := wsproto.As[wsproto.ReloadResult](waitFrame(t, frames, wsproto.TypeReloadResult))
	if err != nil {
		t.Fatalf("decode reload_result: %v", err)
	}
	if res.OK || !strings.Contains(res.Err, "not wired") {
		t.Fatalf("unwired reload must fail with a reason: %+v", res)
	}
}
