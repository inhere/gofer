package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/pushhub"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/workerenroll"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// fakeCore is a config holder with core.Update's clone->mutate->publish shape.
type fakeCore struct {
	mu  sync.Mutex
	cfg *config.Config
}

func (f *fakeCore) Update(mut func(*config.Config) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := f.cfg.Clone()
	if err := mut(next); err != nil {
		return err
	}
	f.cfg = next
	return nil
}

func (f *fakeCore) Config() *config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

type fakeHub struct {
	mu   sync.Mutex
	disc []string
}

func (h *fakeHub) DisconnectWorker(id string) {
	h.mu.Lock()
	h.disc = append(h.disc, id)
	h.mu.Unlock()
}

func enrollStore(t *testing.T) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestEnrollment(t *testing.T, st *jobstore.Store, core *fakeCore, hub enrollHub) *enrollment {
	t.Helper()
	e, err := newEnrollment(st, enrollRegistrar{core: core, hub: hub}, core.Config)
	if err != nil {
		t.Fatal(err)
	}
	// Registered after the store-close cleanup, so it runs first (G051).
	t.Cleanup(e.Close)
	return e
}

func enrollCfg(enabled bool) *config.Config {
	cfg := &config.Config{}
	cfg.Server.WorkerEnrollment = config.WorkerEnrollmentConfig{Enabled: enabled}
	return cfg
}

func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// acceptNew files and accepts a request for worker id; it returns the enrollment id.
func acceptNew(t *testing.T, e *enrollment, id, secret string) string {
	t.Helper()
	r, err := e.svc.Request(context.Background(), workerenroll.RequestInput{Source: "10.0.0.1", SecretHash: hashOf(secret)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Accept(context.Background(), r.EnrollID, workerenroll.AcceptInput{PairingCode: r.PairingCode, WorkerID: id, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	return r.EnrollID
}

func TestEnrollConfigSourceReadsLiveSnapshot(t *testing.T) {
	core := &fakeCore{cfg: enrollCfg(false)}
	src := enrollConfig{current: core.Config}
	if src.Enrollment().Enabled {
		t.Fatal("enabled should start false")
	}
	_ = core.Update(func(c *config.Config) error { c.Server.WorkerEnrollment.Enabled = true; return nil })
	if !src.Enrollment().Enabled {
		t.Fatal("source captured the old snapshot")
	}
	if _, ok := src.WorkerToken("w1"); ok {
		t.Fatal("unknown worker has no token")
	}
}

func TestEnrollmentDisabledStillSweepsAndRevokes(t *testing.T) {
	st := enrollStore(t)
	core := &fakeCore{cfg: enrollCfg(true)}
	hub := &fakeHub{}
	e := newTestEnrollment(t, st, core, hub)
	id := acceptNew(t, e, "w1", "s")

	// Disable at runtime, then let the collect window lapse on a fake clock via a
	// second service sharing the same store/config.
	_ = core.Update(func(c *config.Config) error { c.Server.WorkerEnrollment.Enabled = false; return nil })
	now := time.Now().Add(10 * time.Minute)
	svc, err := workerenroll.New(workerenroll.Options{
		Store: st, Registrar: enrollRegistrar{core: core, hub: hub}, Audit: enrollAudit{store: st},
		Config: enrollConfig{current: core.Config}, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sweep(false); err != nil {
		t.Fatal(err)
	}
	row, _ := svc.Get(id)
	if row.State != jobstore.EnrollExpired {
		t.Fatalf("state=%s, want expired", row.State)
	}
	if _, ok := core.Config().Server.Workers["w1"]; ok {
		t.Fatal("registration should have been revoked while disabled")
	}
	if len(hub.disc) != 1 || hub.disc[0] != "w1" {
		t.Fatalf("disconnects=%v", hub.disc)
	}
	if evs, _ := st.ListAuditEvents(workerenroll.AuditExpire, id); len(evs) != 1 {
		t.Fatalf("expire audit rows=%d", len(evs))
	}
}

func TestStartupReconcileFinishesRegisteredAccepting(t *testing.T) {
	st := enrollStore(t)
	core := &fakeCore{cfg: enrollCfg(true)}
	tok, _ := config.NewWorkerToken()
	_ = core.Update(func(c *config.Config) error { return config.RegisterWorkerInto(c, "w9", nil, nil, tok) })
	now := time.Now().Unix()
	if _, err := st.InsertWorkerEnrollment(jobstore.WorkerEnrollment{ID: "we_x", SecretHash: hashOf("s"), PairingCode: "ABCD2345",
		Source: "s", CreatedAt: now, DecideUntil: now + 120}, 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimWorkerEnrollment(jobstore.WorkerEnrollmentClaim{ID: "we_x", Now: now, WorkerID: "w9",
		LabelsJSON: "[]", ProjectsJSON: "[]", TokenFP: config.WorkerTokenFingerprint(tok), DecidedBy: "admin"}); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	newTestEnrollment(t, st, core, &fakeHub{}) // runs Reconcile
	row, err := st.GetWorkerEnrollment("we_x")
	if err != nil || row.State != jobstore.EnrollAccepted || row.CollectUntil <= now {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}

// TestPresenceFanOutReachesEnrollmentAndLivePush connects a worker to a real hub whose
// single presence observer is installed by wireLivePush: the enrollment collector gets
// the first-connect event AND the push hub still emits its `runners` invalidation.
func TestPresenceFanOutReachesEnrollmentAndLivePush(t *testing.T) {
	st := enrollStore(t)
	core := &fakeCore{cfg: enrollCfg(true)}
	e := newTestEnrollment(t, st, core, &fakeHub{})
	id := acceptNew(t, e, "w1", "s")

	hub := wshub.New(map[string]string{"w1": "w1"})
	ph := pushhub.New(pushhub.Options{InvalInterval: 10 * time.Millisecond, StatsInterval: 10 * time.Millisecond})
	wireLivePush(ph, st, nil, hub, nil, e.collector.Observe)
	browser, err := ph.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(browser.Close)
	browser.Subscribe([]string{pushhub.TopicRunners}, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workers/connect", func(w http.ResponseWriter, r *http.Request) { hub.Accept(w, r, "w1") })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 15*time.Second))
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/workers/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "done") })
	payload, _ := json.Marshal(wsproto.Register{WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion})
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegister, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var ack wsproto.Envelope
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		t.Fatal(err)
	}

	wait.Until(t, 10*time.Second, "enrollment collected by hub first connect", func() bool {
		row, err := st.GetWorkerEnrollment(id)
		return err == nil && row.State == jobstore.EnrollCollected
	})
	expectFrame(t, browser, "inval", "runners") // livepush's behaviour is unchanged
}
