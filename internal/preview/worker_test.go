package preview

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/wsproto"
)

// fakeWorkerHub plays both the hub (liveness, protocol, the preview_open frame) and the
// worker (it dials 127.0.0.1:<port> and calls back on the preview-connect endpoint, which
// is served by Manager.AcceptWorker), so the whole server half runs for real.
type fakeWorkerHub struct {
	m        *Manager
	callback string // ws URL of the AcceptWorker endpoint

	mu      sync.Mutex
	inst    string
	proto   int
	online  bool
	code    string                      // error code the "worker" answers with
	tamper  func(*wsproto.PreviewHello) // rewrites the hello before it is sent
	opens   []wsproto.PreviewOpen
	openErr error
}

func (h *fakeWorkerHub) LiveInstance(string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inst, h.online
}

func (h *fakeWorkerHub) WorkerProtocol(string) (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proto, h.online
}

func (h *fakeWorkerHub) OpenPreview(_ string, o wsproto.PreviewOpen) error {
	h.mu.Lock()
	err := h.openErr
	h.opens = append(h.opens, o)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	go h.serveOpen(o)
	return nil
}

func (h *fakeWorkerHub) set(f func()) { h.mu.Lock(); f(); h.mu.Unlock() }

func (h *fakeWorkerHub) serveOpen(o wsproto.PreviewOpen) {
	h.mu.Lock()
	code, tamper := h.code, h.tamper
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var nc net.Conn
	if code == "" {
		var err error
		if nc, err = net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port))); err != nil {
			code = wsproto.PreviewCodeDialFailed
		} else {
			defer nc.Close()
		}
	}
	ws, _, err := websocket.Dial(ctx, h.callback, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	hello := wsproto.PreviewHello{PreviewID: o.PreviewID, JobID: o.JobID, ConnID: o.ConnID, RelayNonce: o.RelayNonce, ErrorCode: code}
	if tamper != nil {
		tamper(&hello)
	}
	if wsjson.Write(ctx, ws, hello) != nil || code != "" {
		return
	}
	tunnel.BridgeWithOptions(ctx, ws, nc, tunnel.BridgeOptions{})
}

func newWorkerManager(t *testing.T, n int) (*Manager, *fakeWorkerHub, []int) {
	t.Helper()
	m, ports := newManager(t, n)
	h := &fakeWorkerHub{m: m, inst: "inst-1", proto: wsproto.CurrentProtocolVersion, online: true}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		m.AcceptWorker(r.Context(), "w1", c)
	}))
	t.Cleanup(relay.Close)
	h.callback = "ws" + strings.TrimPrefix(relay.URL, "http")
	m.AttachWorkers(h)
	return m, h, ports
}

// createOnWorker opens a lease on worker w1 and trades the token for the cookie value.
func createOnWorker(t *testing.T, m *Manager, jobID string, target int) (lease Lease, base, session string) {
	t.Helper()
	l, raw, err := m.CreateOnWorker(context.Background(), "w1", jobID, "caller-a", "127.0.0.1", target, 60, true)
	if err != nil {
		t.Fatalf("CreateOnWorker: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	base = u.Scheme + "://" + u.Host
	return l, base, exchangeToken(t, base, u.Query().Get("token"))
}

func TestWorkerLeaseServesHTTP(t *testing.T) {
	m, h, _ := newWorkerManager(t, 1)
	tp := okTargetPort(t)
	l, base, session := createOnWorker(t, m, "job-1", tp)
	if l.WorkerID != "w1" {
		t.Fatalf("lease worker = %q, want w1", l.WorkerID)
	}
	resp := get(t, base+"/page", map[string]string{"Cookie": "gofer_preview=" + session})
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hello /page" {
		t.Fatalf("status=%d body=%q, want 200 hello /page", resp.StatusCode, body)
	}
	h.mu.Lock()
	o := h.opens[0]
	h.mu.Unlock()
	if o.JobID != "job-1" || o.PreviewID != l.ID || o.InstanceID != "inst-1" || o.Port != tp {
		t.Fatalf("preview_open = %+v", o)
	}
	// The cookie of the lease is never forwarded to the application.
	if strings.Contains(fmt.Sprint(o), session) {
		t.Fatal("session leaked into the frame")
	}
}

func TestWorkerLeaseServesLargeBodyIntact(t *testing.T) {
	want := make([]byte, 3<<20)
	for i := range want {
		want[i] = byte(i * 7)
	}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(want) }))
	t.Cleanup(app.Close)
	m, _, _ := newWorkerManager(t, 1)
	_, base, session := createOnWorker(t, m, "job-1", targetPort(t, app))
	resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session})
	got, err := io.ReadAll(resp.Body)
	if err != nil || sha256.Sum256(got) != sha256.Sum256(want) {
		t.Fatalf("body of %d bytes (err=%v) differs from the %d sent", len(got), err, len(want))
	}
}

func TestWorkerLeaseWebSocketAndJobEndCutsIt(t *testing.T) {
	m, _, _ := newWorkerManager(t, 1)
	l, base, session := createOnWorker(t, m, "job-1", targetPort(t, echoTarget(t)))
	c := dialWS(t, base, session)
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, b, err := c.Read(ctx); err != nil || string(b) != "echo:ping" {
		t.Fatalf("read=%q err=%v", b, err)
	}
	m.CloseJob("job-1")
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("the socket must be cut when the job ends")
	}
	got, _, _ := m.Get(l.ID)
	if got.State != "closed" || got.CloseReason != "job_ended" {
		t.Fatalf("lease = %+v, want closed/job_ended", got)
	}
}

func TestWorkerOutdatedAndOfflineAreRefusedBeforeAllocation(t *testing.T) {
	m, h, _ := newWorkerManager(t, 1)
	tp := okTargetPort(t)
	h.set(func() { h.proto = wsproto.PreviewMinProtocolVersion - 1 })
	_, _, err := m.CreateOnWorker(context.Background(), "w1", "job-1", "c", "", tp, 60, true)
	if !errors.Is(err, ErrWorkerOutdated) || !strings.Contains(err.Error(), "29") {
		t.Fatalf("err=%v, want ErrWorkerOutdated naming v29", err)
	}
	h.set(func() { h.online = false })
	if _, _, err = m.CreateOnWorker(context.Background(), "w1", "job-1", "c", "", tp, 60, true); !errors.Is(err, ErrWorkerOffline) {
		t.Fatalf("err=%v, want ErrWorkerOffline", err)
	}
	if vs, _ := m.List("job-1"); len(vs) != 0 {
		t.Fatalf("a refused create left leases: %+v", vs)
	}
	// The single pool port is still free.
	h.set(func() { h.proto, h.online = wsproto.CurrentProtocolVersion, true })
	createOnWorker(t, m, "job-1", tp)
}

func TestWorkerGoneClosesLeasesWithWorkerLost(t *testing.T) {
	m, h, _ := newWorkerManager(t, 2)
	tp := targetPort(t, echoTarget(t))
	a, base, session := createOnWorker(t, m, "job-1", tp)
	c := dialWS(t, base, session)
	local, _, _ := create(t, m, "job-2", okTargetPort(t), 60)

	h.set(func() { h.online = false })
	m.NotePresence("w1", false)

	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("open socket must be cut when the worker goes away")
	}
	if got, _, _ := m.Get(a.ID); got.State != "closed" || got.CloseReason != "worker_lost" {
		t.Fatalf("worker lease = %+v, want closed/worker_lost", got)
	}
	if got, _, _ := m.Get(local.ID); got.State != "active" {
		t.Fatalf("a local lease must not be touched, got %+v", got)
	}
}

func TestWorkerRestartedAsNewInstanceIsNeverRebound(t *testing.T) {
	m, h, _ := newWorkerManager(t, 1)
	l, base, session := createOnWorker(t, m, "job-1", okTargetPort(t))
	h.set(func() { h.inst = "inst-2" }) // same worker id, different process
	resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 once the instance changed", resp.StatusCode)
	}
	wait.For(t, 10*time.Second, "lease closed as worker_lost", func() (bool, any) {
		got, _, _ := m.Get(l.ID)
		return got.State == "closed" && got.CloseReason == "worker_lost", got
	})
	h.mu.Lock()
	n := len(h.opens)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d preview_open frames reached the new instance", n)
	}
}

func TestPresenceOnlineWithNewInstanceClosesOldLeases(t *testing.T) {
	m, h, _ := newWorkerManager(t, 1)
	l, _, _ := createOnWorker(t, m, "job-1", okTargetPort(t))
	h.set(func() { h.inst = "inst-2" })
	m.NotePresence("w1", true)
	if got, _, _ := m.Get(l.ID); got.State != "closed" || got.CloseReason != "worker_lost" {
		t.Fatalf("lease = %+v, want closed/worker_lost", got)
	}
}

func TestWorkerRefusalAnswers502(t *testing.T) {
	for _, code := range []string{wsproto.PreviewCodeJobNotRunning, wsproto.PreviewCodeDialFailed, wsproto.PreviewCodeInstance} {
		t.Run(code, func(t *testing.T) {
			m, h, _ := newWorkerManager(t, 1)
			h.set(func() { h.code = code })
			_, base, session := createOnWorker(t, m, "job-1", okTargetPort(t))
			resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session})
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status=%d, want 502", resp.StatusCode)
			}
		})
	}
}

func TestWorkerFrameSendFailureAnswers502AndCancelsRendezvous(t *testing.T) {
	m, h, _ := newWorkerManager(t, 1)
	_, base, session := createOnWorker(t, m, "job-1", okTargetPort(t))
	h.set(func() { h.openErr = tunnel.ErrWorkerOffline })
	resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", resp.StatusCode)
	}
}

// A callback whose hello names another lease / job / connection is refused: the
// connection never reaches the proxy, which answers 502 once the rendezvous times out.
func TestAcceptWorkerRefusesMismatchedHello(t *testing.T) {
	old := relayTimeout
	relayTimeout = 500 * time.Millisecond
	t.Cleanup(func() { relayTimeout = old })
	cases := map[string]func(*wsproto.PreviewHello){
		"other lease": func(h *wsproto.PreviewHello) { h.PreviewID = "other" },
		"other job":   func(h *wsproto.PreviewHello) { h.JobID = "job-2" },
		"other conn":  func(h *wsproto.PreviewHello) { h.ConnID = "t-other" },
		"bad nonce":   func(h *wsproto.PreviewHello) { h.RelayNonce = "nope" },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			m, h, _ := newWorkerManager(t, 1)
			h.set(func() { h.tamper = tamper })
			_, base, session := createOnWorker(t, m, "job-1", okTargetPort(t))
			resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session})
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status=%d, want 502", resp.StatusCode)
			}
		})
	}
}

func TestAcceptWorkerNonceIsSingleUseAndBoundToTheWorker(t *testing.T) {
	m, h, _ := newWorkerManager(t, 1)
	_, base, session := createOnWorker(t, m, "job-1", okTargetPort(t))
	get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session})
	h.mu.Lock()
	o := h.opens[0]
	h.mu.Unlock()

	// Replaying the consumed nonce is refused.
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	ws, _, err := websocket.Dial(ctx, h.callback, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	_ = wsjson.Write(ctx, ws, wsproto.PreviewHello{PreviewID: o.PreviewID, JobID: o.JobID, ConnID: o.ConnID, RelayNonce: o.RelayNonce})
	if _, _, err := ws.Read(ctx); websocket.CloseStatus(err) != 4401 {
		t.Fatalf("replay err=%v, want close 4401", err)
	}

	// A different worker presenting a fresh nonce is refused too.
	p, nonce := m.relay.Begin(tunnel.Binding{WorkerID: "w-other", InstanceID: "inst-1", CallerID: "p", Target: "j"}, time.Minute)
	defer p.Cancel()
	ws2, _, err := websocket.Dial(ctx, h.callback, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws2.CloseNow()
	_ = wsjson.Write(ctx, ws2, wsproto.PreviewHello{PreviewID: "p", JobID: "j", ConnID: p.TunnelID(), RelayNonce: nonce})
	if _, _, err := ws2.Read(ctx); websocket.CloseStatus(err) != 4409 {
		t.Fatalf("other worker err=%v, want close 4409", err)
	}
}

// The per-lease connection cap and the idle reclaim are the P1 rules; they must apply to a
// lease on a worker as they do locally.
func TestWorkerLeaseKeepsConnectionCapAndIdleReclaim(t *testing.T) {
	m, _, _ := newWorkerManager(t, 1)
	m.idle = 300 * time.Millisecond
	l, base, session := createOnWorker(t, m, "job-1", targetPort(t, echoTarget(t)))
	c := dialWS(t, base, session)
	wait.For(t, 10*time.Second, "idle connections reclaimed", func() (bool, any) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, _, err := c.Read(ctx)
		return err != nil && ctx.Err() == nil, err
	})
	if got, _, _ := m.Get(l.ID); got.State != "active" {
		t.Fatalf("idle must not end the lease, got %q", got.State)
	}
}

func TestWorkerLeaseCloseCutsUpstreamRelays(t *testing.T) {
	m, _, _ := newWorkerManager(t, 1)
	l, base, session := createOnWorker(t, m, "job-1", targetPort(t, echoTarget(t)))
	dialWS(t, base, session)
	lv := func() *live { m.mu.Lock(); defer m.mu.Unlock(); return m.lives[l.ID] }()
	wait.For(t, 10*time.Second, "an upstream relay", func() (bool, any) {
		lv.mu.Lock()
		defer lv.mu.Unlock()
		return len(lv.upstream) == 1, len(lv.upstream)
	})
	if err := m.Revoke(l.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	lv.mu.Lock()
	n := len(lv.upstream)
	lv.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d upstream relays left after revoke", n)
	}
}
