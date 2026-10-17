package workerenroll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeEnv doubles as Registrar, AuditSink, ConfigSource, Limiter and SourceBlocker.
type fakeEnv struct {
	mu       sync.Mutex
	settings Settings
	tokens   map[string]string // the "config": worker id -> token
	regErr   error
	onReg    func()
	audit    []string // kind|id|actor
	removed  []string
	disc     []string
	blocked  map[string]time.Duration
	limitErr error
}

func (f *fakeEnv) Register(id string, labels, projects []string, token string) error {
	if f.onReg != nil {
		f.onReg()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.regErr != nil {
		return f.regErr
	}
	if _, ok := f.tokens[id]; ok {
		return config.ErrWorkerAlreadyRegistered
	}
	f.tokens[id] = token
	return nil
}

func (f *fakeEnv) Remove(id, fp string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok, ok := f.tokens[id]
	if !ok || config.WorkerTokenFingerprint(tok) != fp {
		return false, nil
	}
	delete(f.tokens, id)
	f.removed = append(f.removed, id)
	return true, nil
}

func (f *fakeEnv) Disconnect(id string) {
	f.mu.Lock()
	f.disc = append(f.disc, id)
	f.mu.Unlock()
}

func (f *fakeEnv) Record(kind, id, actor string) {
	f.mu.Lock()
	f.audit = append(f.audit, kind+"|"+id+"|"+actor)
	f.mu.Unlock()
}

func (f *fakeEnv) Enrollment() Settings { return f.settings }

func (f *fakeEnv) WorkerToken(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	return t, ok
}

func (f *fakeEnv) Allow(string, time.Time) error { return f.limitErr }

func (f *fakeEnv) Block(src string, d time.Duration) {
	f.mu.Lock()
	f.blocked[src] = d
	f.mu.Unlock()
}

func (f *fakeEnv) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.audit))
	for i, a := range f.audit {
		out[i] = strings.SplitN(a, "|", 2)[0]
	}
	return out
}

func (f *fakeEnv) count(kind string) int {
	n := 0
	for _, k := range f.kinds() {
		if k == kind {
			n++
		}
	}
	return n
}

type harness struct {
	svc   *Service
	env   *fakeEnv
	clk   *fakeClock
	store *jobstore.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// Order matters (G051): TempDir first, then the store close, so nothing outlives the db.
	dir := t.TempDir()
	st, err := jobstore.Open(filepath.Join(dir, "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	env := &fakeEnv{
		settings: Settings{Enabled: true, DecideSec: 120, CollectSec: 60, MaxPending: 20},
		tokens:   map[string]string{}, blocked: map[string]time.Duration{},
	}
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	svc, err := New(Options{Store: st, Registrar: env, Audit: env, Config: env, Limiter: env, Blocker: env, Clock: clk.Now})
	assert.NoErr(t, err)
	return &harness{svc: svc, env: env, clk: clk, store: st}
}

func secretOf(n string) (secret, hash string) {
	secret = "secret-" + n
	sum := sha256.Sum256([]byte(secret))
	return secret, hex.EncodeToString(sum[:])
}

// request files a request from source with secret n and returns id, code, secret.
func (h *harness) request(t *testing.T, source, n string) (id, code, secret string) {
	t.Helper()
	secret, hash := secretOf(n)
	r, err := h.svc.Request(context.Background(), RequestInput{Source: source, SecretHash: hash, Labels: []string{"gpu"}})
	assert.NoErr(t, err)
	return r.EnrollID, r.PairingCode, secret
}

func (h *harness) state(t *testing.T, id string) string {
	t.Helper()
	e, err := h.svc.Get(id)
	assert.NoErr(t, err)
	return e.State
}

var bg = context.Background()

func TestPairingCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := NewPairingCode()
		assert.NoErr(t, err)
		assert.Eq(t, 8, len(c))
		for _, r := range c {
			assert.True(t, strings.ContainsRune(pairingAlphabet, r), c)
		}
		seen[c] = true
	}
	assert.Eq(t, 30, len(pairingAlphabet))
	assert.False(t, strings.ContainsAny(pairingAlphabet, "0O1ILU"))
	assert.True(t, len(seen) > 190)
	assert.Eq(t, "K7QXM3PA", NormalizePairingCode(" k7qx-m3pa "))
}

func TestHappyPath(t *testing.T) {
	h := newHarness(t)
	id, code, secret := h.request(t, "10.0.0.1", "a")

	p, err := h.svc.Poll(bg, id, secret)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollPending, p.State)

	res, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: code[:4] + "-" + strings.ToLower(code[4:]), WorkerID: "gpu1",
		Labels: []string{"gpu"}, Projects: []string{"p"}, Actor: "admin"})
	assert.NoErr(t, err)
	assert.Eq(t, "gpu1", res.WorkerID)
	assert.Eq(t, h.clk.Now().Unix()+60, res.CollectUntil)
	assert.Eq(t, jobstore.EnrollAccepted, h.state(t, id))

	// collect is repeatable until confirm, and returns the configured token.
	var first string
	for i := 0; i < 3; i++ {
		p, err = h.svc.Poll(bg, id, secret)
		assert.NoErr(t, err)
		assert.Eq(t, jobstore.EnrollAccepted, p.State)
		assert.Eq(t, "gpu1", p.WorkerID)
		assert.True(t, strings.HasPrefix(p.Token, "gw_"))
		if first == "" {
			first = p.Token
		}
		assert.Eq(t, first, p.Token)
	}
	assert.NoErr(t, h.svc.Confirm(bg, id, secret))
	assert.Eq(t, jobstore.EnrollCollected, h.state(t, id))
	assert.NoErr(t, h.svc.Confirm(bg, id, secret)) // idempotent

	// after confirm the token is no longer handed out.
	p, err = h.svc.Poll(bg, id, secret)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollCollected, p.State)
	assert.Eq(t, "", p.Token)

	assert.Eq(t, []string{AuditRequest, AuditAccept, AuditCollect}, h.env.kinds())
	e, _ := h.svc.Get(id)
	assert.Eq(t, "admin", e.DecidedBy)
	assert.Eq(t, config.WorkerTokenFingerprint(first), e.TokenFP)
	assert.Eq(t, `["p"]`, e.ProjectsJSON)
	for _, a := range h.env.audit { // the token never reaches the audit trail
		assert.False(t, strings.Contains(a, first))
	}
}

func TestFingerprintMismatchGivesNoToken(t *testing.T) {
	h := newHarness(t)
	id, code, secret := h.request(t, "s", "a")
	_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w1", Actor: "admin"})
	assert.NoErr(t, err)

	// someone deletes and re-adds the same id with another token.
	h.env.tokens["w1"] = "gw_other"
	p, err := h.svc.Poll(bg, id, secret)
	assert.Eq(t, ErrTokenMismatch, err)
	assert.Eq(t, "", p.Token)

	// and when the worker is gone altogether.
	delete(h.env.tokens, "w1")
	p, err = h.svc.Poll(bg, id, secret)
	assert.Eq(t, ErrTokenMismatch, err)
	assert.Eq(t, "", p.Token)
}

func TestRejectAndBlockSource(t *testing.T) {
	h := newHarness(t)
	id, _, secret := h.request(t, "evil", "a")
	assert.NoErr(t, h.svc.Reject(bg, id, RejectInput{Reason: "nope", Actor: "admin", BlockSourceMin: 10}))
	assert.Eq(t, 10*time.Minute, h.env.blocked["evil"])
	p, err := h.svc.Poll(bg, id, secret)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollRejected, p.State)
	assert.Eq(t, "nope", p.Reason)
	assert.Eq(t, ErrNotPending, h.svc.Reject(bg, id, RejectInput{Actor: "admin"}))
	_, err = h.svc.Accept(bg, id, AcceptInput{WorkerID: "w", Actor: "admin"})
	assert.Eq(t, ErrNotPending, err)
	assert.Eq(t, 1, h.env.count(AuditReject))

	// no block requested -> blocker untouched.
	id2, _, _ := h.request(t, "meh", "b")
	assert.NoErr(t, h.svc.Reject(bg, id2, RejectInput{Actor: "admin"}))
	_, blocked := h.env.blocked["meh"]
	assert.False(t, blocked)
}

func TestPendingExpiresLazilyAndBySweep(t *testing.T) {
	h := newHarness(t)
	id, code, secret := h.request(t, "s", "a")
	ok, err := h.svc.Expire(id)
	assert.NoErr(t, err)
	assert.False(t, ok) // not due yet

	h.clk.Advance(120 * time.Second)
	// accept loses to expiry: no registration happens.
	_, err = h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w", Actor: "admin"})
	assert.Eq(t, ErrNotPending, err)
	assert.Eq(t, 0, len(h.env.tokens))
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id))
	p, _ := h.svc.Poll(bg, id, secret)
	assert.Eq(t, jobstore.EnrollExpired, p.State)

	id2, _, secret2 := h.request(t, "s2", "b")
	h.clk.Advance(121 * time.Second)
	ok, err = h.svc.Expire(id2)
	assert.NoErr(t, err)
	assert.True(t, ok)
	p, _ = h.svc.Poll(bg, id2, secret2)
	assert.Eq(t, jobstore.EnrollExpired, p.State)
	assert.Eq(t, 2, h.env.count(AuditExpire))
}

// The accept/expiry race: the sweep fires while Register is running. The accepting
// placeholder is not expirable by the pending-only sweep, so accept wins cleanly.
func TestAcceptWinsRaceAgainstExpiry(t *testing.T) {
	h := newHarness(t)
	id, code, _ := h.request(t, "s", "a")
	var swept bool
	h.env.onReg = func() {
		h.clk.Advance(500 * time.Second) // far past decide_until
		swept, _ = h.svc.Expire(id)
	}
	_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w", Actor: "admin"})
	assert.NoErr(t, err)
	assert.False(t, swept)
	assert.Eq(t, jobstore.EnrollAccepted, h.state(t, id))
	assert.Eq(t, 0, h.env.count(AuditExpire))
}

// The other order: expiry wins first, so the claim fails and Register is never called.
func TestExpiryWinsRaceAgainstAccept(t *testing.T) {
	h := newHarness(t)
	id, code, _ := h.request(t, "s", "a")
	h.clk.Advance(200 * time.Second)
	ok, err := h.svc.Expire(id)
	assert.NoErr(t, err)
	assert.True(t, ok)
	called := false
	h.env.onReg = func() { called = true }
	_, err = h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w", Actor: "admin"})
	assert.Eq(t, ErrNotPending, err)
	assert.False(t, called)
}

func TestAcceptedExpiryRevokesOnlyMatchingFingerprint(t *testing.T) {
	h := newHarness(t)
	id, code, secret := h.request(t, "s", "a")
	_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w1", Actor: "admin"})
	assert.NoErr(t, err)
	h.clk.Advance(60 * time.Second)

	// Poll after the window: expired, no token, registration revoked + disconnected.
	p, err := h.svc.Poll(bg, id, secret)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollExpired, p.State)
	assert.Eq(t, "", p.Token)
	assert.Eq(t, []string{"w1"}, h.env.removed)
	assert.Eq(t, []string{"w1"}, h.env.disc)

	// A re-added worker with a different token survives the expiry of an old request.
	id2, code2, _ := h.request(t, "s2", "b")
	_, err = h.svc.Accept(bg, id2, AcceptInput{PairingCode: code2, WorkerID: "w2", Actor: "admin"})
	assert.NoErr(t, err)
	h.env.tokens["w2"] = "gw_someone_elses"
	h.clk.Advance(61 * time.Second)
	ok, err := h.svc.Expire(id2)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id2))
	_, still := h.env.tokens["w2"]
	assert.True(t, still)
	assert.Eq(t, []string{"w1"}, h.env.disc) // no disconnect for the survivor
}

func TestRegisterFailureReturnsToPending(t *testing.T) {
	h := newHarness(t)
	id, code, _ := h.request(t, "s", "a")
	h.env.regErr = errors.New("write config failed")
	_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w", Actor: "admin"})
	assert.Err(t, err)
	assert.Eq(t, "write config failed", err.Error())
	e, _ := h.svc.Get(id)
	assert.Eq(t, jobstore.EnrollPending, e.State)
	assert.Eq(t, "", e.WorkerID)
	assert.Eq(t, "", e.TokenFP)
	assert.Eq(t, 0, h.env.count(AuditAccept))

	// retry with a different id succeeds.
	h.env.regErr = nil
	_, err = h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w-2", Actor: "admin"})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollAccepted, h.state(t, id))

	// duplicate id is reported as the config error and leaves the next request pending.
	id3, code3, _ := h.request(t, "s3", "c")
	_, err = h.svc.Accept(bg, id3, AcceptInput{PairingCode: code3, WorkerID: "w-2", Actor: "admin"})
	assert.True(t, errors.Is(err, config.ErrWorkerAlreadyRegistered))
	assert.Eq(t, jobstore.EnrollPending, h.state(t, id3))
}

func TestSupersedeSameSource(t *testing.T) {
	h := newHarness(t)
	oldID, _, oldSecret := h.request(t, "10.0.0.9", "old")
	newID, _, _ := h.request(t, "10.0.0.9", "new")
	other, _, _ := h.request(t, "10.0.0.10", "other")
	assert.Eq(t, jobstore.EnrollSuperseded, h.state(t, oldID))
	assert.Eq(t, jobstore.EnrollPending, h.state(t, newID))
	assert.Eq(t, jobstore.EnrollPending, h.state(t, other))
	p, err := h.svc.Poll(bg, oldID, oldSecret)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollSuperseded, p.State)
}

func TestGlobalPendingCap(t *testing.T) {
	h := newHarness(t)
	h.env.settings.MaxPending = 2
	h.request(t, "a", "1")
	h.request(t, "b", "2")
	_, hash := secretOf("3")
	_, err := h.svc.Request(bg, RequestInput{Source: "c", SecretHash: hash})
	assert.Eq(t, ErrCapReached, err)
	// a same-source retry supersedes its own request, so it still fits.
	_, hash = secretOf("4")
	_, err = h.svc.Request(bg, RequestInput{Source: "a", SecretHash: hash})
	assert.NoErr(t, err)
}

func TestRequestGuards(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Request(bg, RequestInput{Source: "a", SecretHash: "short"})
	assert.True(t, errors.Is(err, ErrInvalidRequest))

	h.env.limitErr = ErrRateLimited
	_, hash := secretOf("x")
	_, err = h.svc.Request(bg, RequestInput{Source: "a", SecretHash: hash})
	assert.Eq(t, ErrRateLimited, err)
	h.env.limitErr = nil

	h.env.settings.Enabled = false
	_, err = h.svc.Request(bg, RequestInput{Source: "a", SecretHash: hash})
	assert.Eq(t, ErrDisabled, err)
	assert.Eq(t, 0, h.env.count(AuditRequest))
}

func TestPairingCodeAttemptLimit(t *testing.T) {
	h := newHarness(t)
	id, code, secret := h.request(t, "s", "a")
	for i := 0; i < MaxCodeFailures-1; i++ {
		_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: "AAAAAAAA", WorkerID: "w", Actor: "admin"})
		assert.Eq(t, ErrCodeMismatch, err)
		assert.False(t, strings.Contains(err.Error(), code)) // never echoed
	}
	assert.Eq(t, jobstore.EnrollPending, h.state(t, id))
	_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: "AAAAAAAA", WorkerID: "w", Actor: "admin"})
	assert.Eq(t, ErrCodeMismatch, err)
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id))
	// even the right code is too late now.
	_, err = h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w", Actor: "admin"})
	assert.Eq(t, ErrNotPending, err)
	assert.Eq(t, 0, len(h.env.tokens))
	assert.Eq(t, MaxCodeFailures, h.env.count(AuditCodeMismatch))
	p, _ := h.svc.Poll(bg, id, secret)
	assert.Eq(t, jobstore.EnrollExpired, p.State)
}

func TestSecretAttemptLimit(t *testing.T) {
	h := newHarness(t)
	id, _, secret := h.request(t, "s", "a")
	for i := 0; i < MaxSecretFailures-1; i++ {
		_, err := h.svc.Poll(bg, id, "wrong")
		assert.Eq(t, ErrNotFound, err)
	}
	assert.Eq(t, jobstore.EnrollPending, h.state(t, id))
	_, err := h.svc.Poll(bg, id, "wrong")
	assert.Eq(t, ErrNotFound, err)
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id)) // voided
	// the right secret now only sees the dead request; unknown id looks the same as wrong secret.
	p, err := h.svc.Poll(bg, id, secret)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollExpired, p.State)
	_, err = h.svc.Poll(bg, "we_missing", secret)
	assert.Eq(t, ErrNotFound, err)
	assert.Eq(t, MaxSecretFailures, h.env.count(AuditSecretMismatch))
	assert.Eq(t, ErrNotFound, h.svc.Confirm(bg, id, "wrong"))
	assert.Eq(t, ErrNotFound, h.svc.Cancel(bg, id, "wrong"))
}

func TestSecretAttemptLimitVoidsAcceptedAndRevokes(t *testing.T) {
	h := newHarness(t)
	id, code, _ := h.request(t, "s", "a")
	_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w1", Actor: "admin"})
	assert.NoErr(t, err)
	for i := 0; i < MaxSecretFailures; i++ {
		_, err = h.svc.Poll(bg, id, "wrong")
		assert.Eq(t, ErrNotFound, err)
	}
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id))
	assert.Eq(t, []string{"w1"}, h.env.removed)
}

func TestCancel(t *testing.T) {
	h := newHarness(t)
	id, _, secret := h.request(t, "s", "a")
	assert.NoErr(t, h.svc.Cancel(bg, id, secret))
	assert.Eq(t, jobstore.EnrollCancelled, h.state(t, id))
	assert.NoErr(t, h.svc.Cancel(bg, id, secret)) // no-op
	assert.Eq(t, 1, h.env.count(AuditCancel))

	id2, code2, secret2 := h.request(t, "s2", "b")
	_, err := h.svc.Accept(bg, id2, AcceptInput{PairingCode: code2, WorkerID: "w1", Actor: "admin"})
	assert.NoErr(t, err)
	assert.NoErr(t, h.svc.Cancel(bg, id2, secret2))
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id2))
	assert.Eq(t, []string{"w1"}, h.env.removed)
	assert.Eq(t, []string{"w1"}, h.env.disc)
	assert.Eq(t, 2, h.env.count(AuditCancel))
}

func TestConfirmAfterCollectDeadlineIsLenientUntilSwept(t *testing.T) {
	h := newHarness(t)
	id, code, secret := h.request(t, "s", "a")
	_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w1", Actor: "admin"})
	assert.NoErr(t, err)
	h.clk.Advance(90 * time.Second)
	assert.NoErr(t, h.svc.Confirm(bg, id, secret))
	assert.Eq(t, jobstore.EnrollCollected, h.state(t, id))
	assert.Eq(t, 0, len(h.env.removed))
	ok, _ := h.svc.Expire(id)
	assert.False(t, ok) // collected rows are never expired
}

func TestMarkCollectedByWorker(t *testing.T) {
	h := newHarness(t)
	ok, err := h.svc.MarkCollectedByWorker("nobody")
	assert.NoErr(t, err)
	assert.False(t, ok)

	id, code, _ := h.request(t, "s", "a")
	_, err = h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: "w1", Actor: "admin"})
	assert.NoErr(t, err)

	good := h.env.tokens["w1"]
	h.env.tokens["w1"] = "gw_replaced"
	ok, _ = h.svc.MarkCollectedByWorker("w1")
	assert.False(t, ok) // fingerprint differs
	h.env.tokens["w1"] = good
	ok, err = h.svc.MarkCollectedByWorker("w1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.EnrollCollected, h.state(t, id))
}

func TestListAdminHidesNothingButCallerMustStrip(t *testing.T) {
	h := newHarness(t)
	h.request(t, "a", "1")
	id2, _, _ := h.request(t, "b", "2")
	assert.NoErr(t, h.svc.Reject(bg, id2, RejectInput{Actor: "admin"}))
	l, err := h.svc.ListAdmin(24 * time.Hour)
	assert.NoErr(t, err)
	assert.Len(t, l, 2)
	h.clk.Advance(48 * time.Hour)
	l, _ = h.svc.ListAdmin(24 * time.Hour)
	assert.Len(t, l, 1) // the pending one is still listed (and long expired-but-unswept)
}

func TestNewRequiresDependencies(t *testing.T) {
	_, err := New(Options{})
	assert.Err(t, err)
}
