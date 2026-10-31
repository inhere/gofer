package trigger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

const testSecret = "p0-test-secret"

var t0 = time.Unix(100000, 0)

func sign(secret string, ts int64, body []byte) map[string]string {
	mac := hmac.New(sha256.New, []byte(secret))
	stamp := strconv.FormatInt(ts, 10)
	mac.Write([]byte(stamp + "\n"))
	mac.Write(body)
	return map[string]string{"X-Gofer-Trigger-Timestamp": stamp, "X-Gofer-Trigger-Signature": "sha256=" + hex.EncodeToString(mac.Sum(nil))}
}

func testCfg(mut func(*config.TriggerConfig)) *config.Config {
	tc := config.TriggerConfig{Name: "push", Enabled: true, OwnerCaller: "automation", Project: "demo", Template: "source-update",
		Auth:  config.TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_P0"},
		Input: config.TriggerInputConfig{Vars: map[string]string{"ref": "body.ref"}}}
	if mut != nil {
		mut(&tc)
	}
	return &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{{ID: "automation", Token: "x"}}, Triggers: []config.TriggerConfig{tc}}}
}

func openStore(t *testing.T) *jobstore.Store {
	t.Helper()
	s, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type fixture struct {
	eng    *Engine
	store  *jobstore.Store
	now    *time.Time
	mu     sync.Mutex
	audits []jobstore.TriggerAuditEvent
	subs   atomic.Int32
	reqIDs []string
}

func newFixture(t *testing.T, cfg *config.Config) *fixture {
	t.Helper()
	t.Setenv("GOFER_TRIGGER_P0", testSecret)
	f := &fixture{store: openStore(t), now: new(time.Time)}
	*f.now = t0
	f.eng = NewEngine(cfg, f.store, func(_ context.Context, r job.JobRequest) (string, error) {
		n := f.subs.Add(1)
		f.mu.Lock()
		f.reqIDs = append(f.reqIDs, r.RequestID)
		f.mu.Unlock()
		return "job-" + strconv.Itoa(int(n)), nil
	})
	f.eng.SetClock(func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return *f.now })
	f.eng.SetAudit(func(ev jobstore.TriggerAuditEvent) error {
		f.mu.Lock()
		f.audits = append(f.audits, ev)
		f.mu.Unlock()
		return nil
	})
	return f
}

func (f *fixture) advance(d time.Duration) { f.mu.Lock(); *f.now = f.now.Add(d); f.mu.Unlock() }

func (f *fixture) req(body string, extra map[string]string) TriggerRequest {
	f.mu.Lock()
	ts := f.now.Unix()
	f.mu.Unlock()
	h := sign(testSecret, ts, []byte(body))
	for k, v := range extra {
		h[k] = v
	}
	return TriggerRequest{TriggerName: "push", RawBody: []byte(body), Headers: h}
}

func (f *fixture) auditKinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.audits {
		out = append(out, a.Kind)
	}
	return out
}

func TestVerifyHMACRejections(t *testing.T) {
	t.Setenv("GOFER_TRIGGER_P0", testSecret)
	tc := config.TriggerConfig{Auth: config.TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_P0", ReplayWindowSec: 300}}
	body := []byte(`{"ref":"main"}`)
	now := time.Unix(1000, 0)
	good := sign(testSecret, 1000, body)
	if err := VerifyHMAC(tc, body, good, now); err != nil {
		t.Fatalf("valid: %v", err)
	}
	wrong := sign("other-secret", 1000, body)
	if err := VerifyHMAC(tc, body, wrong, now); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong signature = %v", err)
	}
	if err := VerifyHMAC(tc, []byte(`{"ref":"evil"}`), good, now); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("tampered body = %v", err)
	}
	for _, drop := range []string{"X-Gofer-Trigger-Timestamp", "X-Gofer-Trigger-Signature"} {
		h := sign(testSecret, 1000, body)
		delete(h, drop)
		if err := VerifyHMAC(tc, body, h, now); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("missing %s = %v", drop, err)
		}
	}
	// Window edges: exactly the window is accepted, one second more is a replay.
	if err := VerifyHMAC(tc, body, good, now.Add(300*time.Second)); err != nil {
		t.Fatalf("at the window edge: %v", err)
	}
	if err := VerifyHMAC(tc, body, good, now.Add(301*time.Second)); !errors.Is(err, ErrReplay) {
		t.Fatalf("past the window = %v", err)
	}
	if err := VerifyHMAC(tc, body, good, now.Add(-301*time.Second)); !errors.Is(err, ErrReplay) {
		t.Fatalf("future stamp = %v", err)
	}
	t.Setenv("GOFER_TRIGGER_P0", "")
	if err := VerifyHMAC(tc, body, good, now); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unset secret = %v", err)
	}
}

func TestInvokeRejectsOversizedBody(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Limits.MaxBodyBytes = 16 }))
	_, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main","pad":"0123456789"}`, nil))
	if !errors.Is(err, ErrBodyTooLarge) || f.subs.Load() != 0 {
		t.Fatalf("err=%v subs=%d", err, f.subs.Load())
	}
}

func TestDedupKeyFromConfiguredReference(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		body  string
		extra map[string]string
	}{
		{"header", "header.X-Delivery", `{"ref":"main"}`, map[string]string{"X-Delivery": "d-1"}},
		{"body", "body.delivery.id", `{"ref":"main","delivery":{"id":"d-1"}}`, nil},
		{"idempotency", "Idempotency-Key", `{"ref":"main"}`, map[string]string{"Idempotency-Key": "d-1"}},
		{"unconfigured idempotency header", "", `{"ref":"main"}`, map[string]string{"Idempotency-Key": "d-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = c.key }))
			first, err := f.eng.Invoke(context.Background(), f.req(c.body, c.extra))
			if err != nil || first.Duplicate || first.JobID != "job-1" {
				t.Fatalf("first=%+v err=%v", first, err)
			}
			second, err := f.eng.Invoke(context.Background(), f.req(c.body, c.extra))
			if err != nil || !second.Duplicate || second.JobID != "job-1" || f.subs.Load() != 1 {
				t.Fatalf("second=%+v err=%v subs=%d", second, err, f.subs.Load())
			}
		})
	}
}

func TestMissingEventIDDeliversAtLeastOnce(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = "header.X-Delivery" }))
	for i := 0; i < 2; i++ {
		res, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, nil))
		if err != nil || res.Duplicate || res.JobID == "" {
			t.Fatalf("call %d: %+v err=%v", i, res, err)
		}
	}
	if f.subs.Load() != 2 {
		t.Fatalf("subs=%d, want 2 (no dedup without an event id)", f.subs.Load())
	}
}

func TestSameEventDifferentBodyConflicts(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = "header.X-Delivery" }))
	h := map[string]string{"X-Delivery": "d-1"}
	if _, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, h)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"dev"}`, h)); !errors.Is(err, jobstore.ErrTriggerReceiptConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestAuditHashesEventIDAndCarriesProviderProject(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = "header.X-Delivery" }))
	const raw = "delivery-raw-id-123"
	r := f.req(`{"ref":"main"}`, map[string]string{"X-Delivery": raw})
	r.Provider, r.RemoteAddr = "github", "203.0.113.9:4444"
	if _, err := f.eng.Invoke(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	want := jobstore.HashTriggerEventID("push", raw)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.audits) < 2 {
		t.Fatalf("audits=%v", f.audits)
	}
	for _, a := range f.audits {
		if a.EventHash != want || a.EventHash == raw || a.Provider != "github" || a.Project != "demo" || a.Owner != "automation" {
			t.Fatalf("audit=%+v", a)
		}
		if a.Remote == "" || a.Remote == r.RemoteAddr {
			t.Fatalf("remote not digested: %q", a.Remote)
		}
	}
	// The receipt key is the hash too: the raw id is nowhere in the store.
	if _, ok, _ := f.store.GetTriggerReceipt("push", raw); ok {
		t.Fatal("receipt stored under the raw event id")
	}
	if _, ok, _ := f.store.GetTriggerReceipt("push", want); !ok {
		t.Fatal("receipt not stored under the hashed id")
	}
}

func TestFailedSubmitIsRetriedByRedelivery(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = "header.X-Delivery" }))
	var fail atomic.Bool
	fail.Store(true)
	f.eng.submit = func(_ context.Context, r job.JobRequest) (string, error) {
		f.mu.Lock()
		f.reqIDs = append(f.reqIDs, r.RequestID)
		f.mu.Unlock()
		if fail.Load() {
			return "", errors.New("store unavailable")
		}
		return "job-ok", nil
	}
	h := map[string]string{"X-Delivery": "d-1"}
	if _, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, h)); err == nil {
		t.Fatal("expected the transient failure")
	}
	fail.Store(false)
	res, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, h))
	if err != nil || res.Duplicate || res.JobID != "job-ok" {
		t.Fatalf("redelivery res=%+v err=%v", res, err)
	}
	if len(f.reqIDs) != 2 || f.reqIDs[0] != f.reqIDs[1] {
		t.Fatalf("request ids %v: the retry must reuse the first attempt's idempotency key", f.reqIDs)
	}
	again, _ := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, h))
	if !again.Duplicate || again.JobID != "job-ok" {
		t.Fatalf("after success: %+v", again)
	}
}

func TestPermanentRejectionIsNotRetried(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = "header.X-Delivery" }))
	f.eng.submit = func(context.Context, job.JobRequest) (string, error) { return "", job.ErrCeilingDenied }
	h := map[string]string{"X-Delivery": "d-1"}
	if _, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, h)); !errors.Is(err, job.ErrCeilingDenied) {
		t.Fatalf("err=%v", err)
	}
	res, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, h))
	if err != nil || !res.Duplicate || res.Status != jobstore.TriggerReceiptRejected {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// The crash case: the claim exists, the process died before finishing. A fresh claim is
// "in flight" (retryable error, never a success with an empty job id); a stale one is
// taken over by the redelivery.
func TestInterruptedClaimIsTakenOverNotLost(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = "header.X-Delivery" }))
	h := map[string]string{"X-Delivery": "d-1"}
	r := f.req(`{"ref":"main"}`, h)
	key := jobstore.HashTriggerEventID("push", "d-1")
	if _, _, err := f.store.ClaimTriggerReceipt(jobstore.TriggerReceipt{TriggerName: "push", ExternalEventID: key, RequestID: "crashed-req", BodyHash: jobstore.HashTriggerBody(r.RawBody), ReceivedAt: t0.Unix(), ExpiresAt: t0.Unix() + 1000}); err != nil {
		t.Fatal(err)
	}
	res, err := f.eng.Invoke(context.Background(), r)
	if !errors.Is(err, ErrEventInFlight) || res.JobID != "" || f.subs.Load() != 0 {
		t.Fatalf("fresh claim: res=%+v err=%v", res, err)
	}
	f.advance(DefaultClaimTimeout + time.Second)
	res, err = f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, h))
	if err != nil || res.Duplicate || res.JobID != "job-1" {
		t.Fatalf("stale claim: res=%+v err=%v", res, err)
	}
	if f.reqIDs[0] != "crashed-req" {
		t.Fatalf("takeover used request id %q, want the crashed attempt's", f.reqIDs[0])
	}
}

func TestReconcileSettlesStaleClaims(t *testing.T) {
	f := newFixture(t, testCfg(nil))
	body := jobstore.HashTriggerBody([]byte("x"))
	claim := func(id, req string) {
		if _, _, err := f.store.ClaimTriggerReceipt(jobstore.TriggerReceipt{TriggerName: "push", ExternalEventID: id, RequestID: req, BodyHash: body, ReceivedAt: f.now.Unix(), ExpiresAt: t0.Unix() + 100000}); err != nil {
			t.Fatal(err)
		}
	}
	claim("lost", "req-lost")
	claim("created", "req-created")
	if err := f.store.UpsertJob(jobstore.JobRecord{ID: "job-created", ProjectKey: "demo", Agent: "exec", Runner: "local", Status: "queued", Cwd: ".", ResultDir: "/tmp/x", RequestJSON: `{}`, RequestID: "req-created", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// A claim younger than the timeout is left alone.
	f.advance(DefaultClaimTimeout - time.Minute)
	claim("fresh2", "req-fresh2")
	f.advance(2 * time.Minute)
	n, err := f.eng.Reconcile(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("settled=%d err=%v", n, err)
	}
	get := func(id string) jobstore.TriggerReceipt {
		r, ok, err := f.store.GetTriggerReceipt("push", id)
		if err != nil || !ok {
			t.Fatalf("get %s: ok=%v err=%v", id, ok, err)
		}
		return r
	}
	if r := get("lost"); r.Status != jobstore.TriggerReceiptFailed || r.LastError == "" {
		t.Fatalf("lost=%+v", r)
	}
	if r := get("created"); r.Status != jobstore.TriggerReceiptSubmitted || r.JobID != "job-created" {
		t.Fatalf("created=%+v", r)
	}
	if r := get("fresh2"); r.Status != jobstore.TriggerReceiptClaimed {
		t.Fatalf("a claim inside the timeout was settled: %+v", r)
	}
}

func TestPruneReceiptsRemovesOnlyExpired(t *testing.T) {
	f := newFixture(t, testCfg(nil))
	body := jobstore.HashTriggerBody([]byte("x"))
	for id, exp := range map[string]int64{"old": t0.Unix() + 10, "new": t0.Unix() + 1000} {
		if _, _, err := f.store.ClaimTriggerReceipt(jobstore.TriggerReceipt{TriggerName: "push", ExternalEventID: id, RequestID: "r-" + id, BodyHash: body, ReceivedAt: t0.Unix(), ExpiresAt: exp, Status: jobstore.TriggerReceiptSubmitted}); err != nil {
			t.Fatal(err)
		}
	}
	f.advance(11 * time.Second)
	if n, err := f.eng.PruneReceipts(); err != nil || n != 1 {
		t.Fatalf("pruned=%d err=%v", n, err)
	}
	if _, ok, _ := f.store.GetTriggerReceipt("push", "old"); ok {
		t.Fatal("expired receipt kept")
	}
	if _, ok, _ := f.store.GetTriggerReceipt("push", "new"); !ok {
		t.Fatal("live receipt pruned")
	}
}

func TestConcurrentDuplicateSubmitsOnce(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Dedup.Key = "header.X-Delivery" }))
	h := map[string]string{"X-Delivery": "d-1"}
	const n = 16
	var wg sync.WaitGroup
	var ok, inflight atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		r := f.req(`{"ref":"main"}`, h)
		go func() {
			defer wg.Done()
			res, err := f.eng.Invoke(context.Background(), r)
			switch {
			case err == nil && res.JobID != "":
				ok.Add(1)
			case errors.Is(err, ErrEventInFlight):
				inflight.Add(1)
			default:
				t.Errorf("res=%+v err=%v", res, err)
			}
		}()
	}
	wg.Wait()
	if f.subs.Load() != 1 {
		t.Fatalf("submits=%d, want exactly 1", f.subs.Load())
	}
	if ok.Load()+inflight.Load() != n || ok.Load() < 1 {
		t.Fatalf("ok=%d inflight=%d", ok.Load(), inflight.Load())
	}
}

func TestInvokeRateLimitedWithRetryAfter(t *testing.T) {
	f := newFixture(t, testCfg(func(tc *config.TriggerConfig) { tc.Rate = config.TriggerRateConfig{PerMinute: 60, Burst: 2} }))
	for i := 0; i < 2; i++ {
		if _, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, nil)); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	_, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, nil))
	var rl *RateLimitedError
	if !errors.As(err, &rl) || !errors.Is(err, ErrRateLimited) || rl.RetryAfter != time.Second {
		t.Fatalf("err=%v (%+v)", err, rl)
	}
	if f.subs.Load() != 2 {
		t.Fatalf("subs=%d", f.subs.Load())
	}
	f.advance(time.Second)
	if _, err := f.eng.Invoke(context.Background(), f.req(`{"ref":"main"}`, nil)); err != nil {
		t.Fatalf("after refill: %v", err)
	}
	if k := f.auditKinds(); k[len(k)-1] != "trigger.submitted" {
		t.Fatalf("kinds=%v", k)
	}
}

func TestLimiterTokenBucket(t *testing.T) {
	now := t0
	l := NewLimiter(func() time.Time { return now })
	lim := Limit{Key: "k", RatePerSec: 1, Burst: 3}
	for i := 0; i < 3; i++ {
		rel, d := l.Acquire(lim)
		if !d.OK {
			t.Fatalf("burst %d refused", i)
		}
		rel()
	}
	_, d := l.Acquire(lim)
	if d.OK || d.RetryAfter != time.Second {
		t.Fatalf("empty bucket: %+v", d)
	}
	now = now.Add(1500 * time.Millisecond)
	if rel, d := l.Acquire(lim); !d.OK {
		t.Fatalf("after 1.5s: %+v", d)
	} else {
		rel()
	}
	// Half a token is left: the wait is for the other half.
	if _, d := l.Acquire(lim); d.OK || d.RetryAfter != 500*time.Millisecond {
		t.Fatalf("partial token: %+v", d)
	}
	// Long idle refills only up to the burst.
	now = now.Add(time.Hour)
	granted := 0
	for i := 0; i < 10; i++ {
		if _, d := l.Acquire(lim); d.OK {
			granted++
		}
	}
	if granted != 3 {
		t.Fatalf("granted %d after idle, want burst 3", granted)
	}
}

func TestLimiterInflightAndAtomicity(t *testing.T) {
	now := t0
	l := NewLimiter(func() time.Time { return now })
	a := Limit{Key: "a", MaxInflight: 1}
	rel, d := l.Acquire(a)
	if !d.OK {
		t.Fatal("first refused")
	}
	if _, d := l.Acquire(a); d.OK || d.RetryAfter <= 0 {
		t.Fatalf("second admitted or no hint: %+v", d)
	}
	rel()
	rel() // idempotent
	if rel2, d := l.Acquire(a); !d.OK {
		t.Fatalf("after release: %+v", d)
	} else {
		rel2()
	}
	// A refusal by one limit consumes nothing from the others.
	b := Limit{Key: "b", RatePerSec: 1, Burst: 1}
	full := Limit{Key: "full", MaxInflight: 1}
	hold, _ := l.Acquire(full)
	if _, d := l.Acquire(b, full); d.OK {
		t.Fatal("admitted past a full limit")
	}
	hold()
	if _, d := l.Acquire(b, full); !d.OK {
		t.Fatalf("b's token was consumed by a refused request: %+v", d)
	}
}

func TestEffectiveLimitsTakeTheStricterOfTriggerAndOwner(t *testing.T) {
	sc := &config.ServerConfig{Callers: []config.CallerConfig{{ID: "automation", RateLimit: 0.5, RateBurst: 1, MaxConcurrentJobs: 1}}}
	tc := config.TriggerConfig{Name: "push", OwnerCaller: "automation", Rate: config.TriggerRateConfig{PerMinute: 600, Burst: 10, MaxInflight: 5}}
	now := t0
	l := NewLimiter(func() time.Time { return now })
	limits := EffectiveLimits(tc, sc)
	rel, d := l.Acquire(limits...)
	if !d.OK {
		t.Fatal("first refused")
	}
	// The trigger alone would allow 10 in flight and a 10/s rate; the owner's 1 / 0.5 per
	// second wins on both counts.
	if _, d := l.Acquire(limits...); d.OK {
		t.Fatal("owner's in-flight cap ignored")
	}
	rel()
	if _, d := l.Acquire(limits...); d.OK || d.RetryAfter != 2*time.Second {
		t.Fatalf("owner's rate ignored: %+v", d)
	}
	// A trigger cannot loosen the owner: with no owner limits the trigger's own apply.
	if got := EffectiveLimits(tc, &config.ServerConfig{}); got[1].RatePerSec != 0 || got[0].RatePerSec != 10 {
		t.Fatalf("limits=%+v", got)
	}
}
