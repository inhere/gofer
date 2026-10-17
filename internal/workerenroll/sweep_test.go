package workerenroll

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// acceptedRow files and accepts a request and returns its id and the issued token.
func (h *harness) acceptedRow(t *testing.T, source, n, worker string) (id, token string) {
	t.Helper()
	id, code, _ := h.request(t, source, n)
	_, err := h.svc.Accept(bg, id, AcceptInput{PairingCode: code, WorkerID: worker, Actor: "admin"})
	assert.NoErr(t, err)
	token = h.env.tokens[worker]
	assert.NotEmpty(t, token)
	return id, token
}

// accepting leaves a request in the accepting state, as a crash mid-Accept would.
func (h *harness) accepting(t *testing.T, source, n, worker, fp string) string {
	t.Helper()
	id, _, _ := h.request(t, source, n)
	ok, err := h.store.ClaimWorkerEnrollment(jobstore.WorkerEnrollmentClaim{
		ID: id, Now: h.clk.Now().Unix(), WorkerID: worker, LabelsJSON: "[]", ProjectsJSON: "[]", TokenFP: fp, DecidedBy: "admin"})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.EnrollAccepting, h.state(t, id))
	return id
}

func TestSweepExpiresPendingPastDecideUntil(t *testing.T) {
	h := newHarness(t)
	id, _, _ := h.request(t, "10.0.0.1", "a")
	res, err := h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, res.Expired)
	assert.Eq(t, jobstore.EnrollPending, h.state(t, id))
	h.clk.Advance(121 * time.Second)
	res, err = h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, 1, res.Expired)
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id))
	assert.Eq(t, 1, h.env.count(AuditExpire))
}

func TestSweepRevokesUncollectedAccepted(t *testing.T) {
	h := newHarness(t)
	id, _ := h.acceptedRow(t, "10.0.0.1", "a", "w1")
	h.clk.Advance(30 * time.Second)
	_, err := h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollAccepted, h.state(t, id))
	h.clk.Advance(31 * time.Second)
	_, err = h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id))
	_, still := h.env.tokens["w1"]
	assert.False(t, still)
	assert.Eq(t, []string{"w1"}, h.env.disc)
}

func TestSweepDoesNotRevokeWhenFingerprintDiffers(t *testing.T) {
	h := newHarness(t)
	id, _ := h.acceptedRow(t, "10.0.0.1", "a", "w1")
	h.env.tokens["w1"] = "someone-elses-token" // the worker was re-added meanwhile
	h.clk.Advance(61 * time.Second)
	_, err := h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, id))
	assert.Eq(t, "someone-elses-token", h.env.tokens["w1"])
	assert.Eq(t, 0, len(h.env.disc))
}

func TestSweepRunsWhileDisabled(t *testing.T) {
	h := newHarness(t)
	pend, _, _ := h.request(t, "10.0.0.1", "a")
	acc, _ := h.acceptedRow(t, "10.0.0.2", "b", "w2")
	h.env.settings.Enabled = false
	h.clk.Advance(200 * time.Second)
	res, err := h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, 2, res.Expired)
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, pend))
	assert.Eq(t, jobstore.EnrollExpired, h.state(t, acc))
	_, still := h.env.tokens["w2"]
	assert.False(t, still)
	_, err = h.svc.Request(bg, RequestInput{Source: "x", SecretHash: hashOf("z")})
	assert.Eq(t, ErrDisabled, err)
}

func hashOf(n string) string { _, h := secretOf(n); return h }

func TestSweepReconcilesStaleAccepting(t *testing.T) {
	h := newHarness(t)
	tok, _ := config.NewWorkerToken()
	fp := config.WorkerTokenFingerprint(tok)

	// registered before the crash: finishes as accepted with a fresh collect window
	done := h.accepting(t, "10.0.0.1", "a", "w1", fp)
	h.env.tokens["w1"] = tok
	// never registered: back to pending while the decision window is open
	back := h.accepting(t, "10.0.0.2", "b", "w2", fp)

	res, err := h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, res.Reconciled) // younger than 30 s: left alone
	assert.Eq(t, jobstore.EnrollAccepting, h.state(t, done))

	h.clk.Advance(31 * time.Second)
	res, err = h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, 2, res.Reconciled)
	e, _ := h.svc.Get(done)
	assert.Eq(t, jobstore.EnrollAccepted, e.State)
	assert.Eq(t, h.clk.Now().Unix()+60, e.CollectUntil)
	e, _ = h.svc.Get(back)
	assert.Eq(t, jobstore.EnrollPending, e.State)
	assert.Eq(t, "", e.WorkerID)
}

func TestReconcileAtStartup(t *testing.T) {
	tok, _ := config.NewWorkerToken()
	fp := config.WorkerTokenFingerprint(tok)

	t.Run("fingerprint matches", func(t *testing.T) {
		h := newHarness(t)
		id := h.accepting(t, "s1", "a", "w1", fp)
		h.env.tokens["w1"] = tok
		res, err := h.svc.Reconcile() // no age gate at startup
		assert.NoErr(t, err)
		assert.Eq(t, 1, res.Reconciled)
		assert.Eq(t, jobstore.EnrollAccepted, h.state(t, id))
	})
	t.Run("fingerprint differs goes back to pending", func(t *testing.T) {
		h := newHarness(t)
		id := h.accepting(t, "s1", "a", "w1", fp)
		h.env.tokens["w1"] = "other"
		_, err := h.svc.Reconcile()
		assert.NoErr(t, err)
		assert.Eq(t, jobstore.EnrollPending, h.state(t, id))
		assert.Eq(t, "other", h.env.tokens["w1"])
	})
	t.Run("decision window gone expires", func(t *testing.T) {
		h := newHarness(t)
		id := h.accepting(t, "s1", "a", "w1", fp)
		h.clk.Advance(500 * time.Second)
		_, err := h.svc.Reconcile()
		assert.NoErr(t, err)
		assert.Eq(t, jobstore.EnrollExpired, h.state(t, id))
	})
	t.Run("others follow expiry rules", func(t *testing.T) {
		h := newHarness(t)
		pend, _, _ := h.request(t, "s1", "a")
		acc, _ := h.acceptedRow(t, "s2", "b", "w2")
		h.clk.Advance(500 * time.Second)
		_, err := h.svc.Reconcile()
		assert.NoErr(t, err)
		assert.Eq(t, jobstore.EnrollExpired, h.state(t, pend))
		assert.Eq(t, jobstore.EnrollExpired, h.state(t, acc))
	})
}

func TestSweepPrunesEndedAfter30Days(t *testing.T) {
	h := newHarness(t)
	old, _, _ := h.request(t, "s1", "a")
	assert.NoErr(t, h.svc.Reject(bg, old, RejectInput{Actor: "admin"}))
	h.clk.Advance(29 * 24 * time.Hour)
	res, err := h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, res.Pruned)
	h.request(t, "s2", "b") // a fresh request survives the prune below
	h.clk.Advance(2 * 24 * time.Hour)
	res, err = h.svc.Sweep(false)
	assert.NoErr(t, err)
	assert.Eq(t, 1, res.Pruned)
	_, err = h.svc.Get(old)
	assert.Eq(t, ErrNotFound, err)
}

func TestSweeperLoopExpiresAndStopsBeforeStoreClose(t *testing.T) {
	h := newHarness(t)
	id, _, _ := h.request(t, "10.0.0.1", "a")
	h.clk.Advance(200 * time.Second)
	sw := NewSweeper(h.svc, 5*time.Millisecond)
	sw.Start()
	// G051: registered after the store-close cleanup, so it runs first.
	t.Cleanup(sw.Close)
	wait.Until(t, 10*time.Second, "pending request expired by the loop", func() bool {
		return h.state(t, id) == jobstore.EnrollExpired
	})
	sw.Close()
	sw.Close() // idempotent
}

func TestCollectorMarksAcceptedCollected(t *testing.T) {
	h := newHarness(t)
	id, _ := h.acceptedRow(t, "10.0.0.1", "a", "w1")
	c := NewCollector(h.svc, 4)
	t.Cleanup(c.Close)
	c.Observe("w1", false) // offline events are ignored
	c.Observe("nobody", true)
	assert.Eq(t, jobstore.EnrollAccepted, h.state(t, id))
	c.Observe("w1", true)
	wait.Until(t, 10*time.Second, "collected by first connect", func() bool {
		return h.state(t, id) == jobstore.EnrollCollected
	})
	assert.Eq(t, 1, h.env.count(AuditCollect))
}

func TestCollectorSkipsOnFingerprintMismatchAndDropsWhenFull(t *testing.T) {
	h := newHarness(t)
	id, _ := h.acceptedRow(t, "10.0.0.1", "a", "w1")
	h.env.tokens["w1"] = "replaced"
	c := NewCollector(h.svc, 1)
	t.Cleanup(c.Close)
	c.Observe("w1", true)
	c.Close() // drained or not, it must never block; row stays accepted
	for i := 0; i < 10; i++ {
		c.Observe("w1", true) // closed worker, full queue: must not block
	}
	assert.Eq(t, jobstore.EnrollAccepted, h.state(t, id))
}

func TestEnabledSwitchedAtRuntime(t *testing.T) {
	h := newHarness(t)
	h.request(t, "10.0.0.1", "a")
	h.env.mu.Lock()
	h.env.settings.Enabled = false
	h.env.mu.Unlock()
	_, err := h.svc.Request(bg, RequestInput{Source: "10.0.0.9", SecretHash: hashOf("b")})
	assert.Eq(t, ErrDisabled, err)
	h.env.settings.Enabled = true
	_, err = h.svc.Request(bg, RequestInput{Source: "10.0.0.9", SecretHash: hashOf("b")})
	assert.NoErr(t, err)
}
