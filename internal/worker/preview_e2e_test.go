// preview_e2e_test.go is the job-preview P2 end-to-end matrix: a real httptest serve (hub +
// preview manager + httpapi) and a real worker.Client over the hub websocket, with the
// previewed application an in-process HTTP server on loopback. It covers HTTP, WebSocket,
// job end, worker loss, an old-protocol worker and that no secret reaches the logs.
package worker_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wsproto"
)

// previewApp is the application a job "listens" with: plain pages, a large binary, a
// redirect and a WebSocket echo. wsOpen counts the sockets its handlers still serve, which
// drops to zero exactly when the worker's relay to it is closed.
type previewApp struct {
	srv    *httptest.Server
	port   string
	wsOpen atomic.Int32
}

func startPreviewApp(t *testing.T) *previewApp {
	t.Helper()
	a := &previewApp{}
	mux := http.NewServeMux()
	mux.HandleFunc("/bin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(previewBlob()) })
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/target", http.StatusFound) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		a.wsOpen.Add(1)
		defer a.wsOpen.Add(-1)
		defer c.CloseNow()
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if c.Write(r.Context(), typ, append([]byte("echo:"), b...)) != nil {
				return
			}
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "hello "+r.URL.Path) })
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	u, _ := url.Parse(a.srv.URL)
	a.port = u.Port()
	return a
}

func previewBlob() []byte {
	b := make([]byte, 2<<20)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	return b
}

type previewLease struct {
	ID, URL, Base, Token string
	Port                 int
	Session              string
}

// previewE2E is a hub with preview enabled, a connected worker and one running remote job.
type previewE2E struct {
	hub    *hubSide
	jobID  string
	app    *previewApp
	stopWk context.CancelFunc
}

func startPreviewE2E(t *testing.T) *previewE2E {
	t.Helper()
	hub := buildHubSide(t)
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	cl := buildWorkerSide(t, hub.ts.URL)
	worker.StartClient(t, ctx, cl)
	waitWorkerOnline(t, hub.hub)
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: testcmd.Cmd(t, "sleep", "120s"), Cwd: ".", TimeoutSec: 300,
	})
	waitRemoteSnapshot(t, hub.jobs, created.ID, func(g job.JobResult) bool { return g.Status == job.StatusRunning })
	t.Cleanup(func() { _ = hub.jobs.Cancel(created.ID); hub.jobs.Wait(created.ID) })
	return &previewE2E{hub: hub, jobID: created.ID, app: startPreviewApp(t), stopWk: stop}
}

func (e *previewE2E) api(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.hub.ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer server-default-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// openLease creates a lease through the HTTP API and trades its token for the cookie.
func (e *previewE2E) openLease(t *testing.T, target string) previewLease {
	t.Helper()
	port := 0
	if target != "" {
		port = atoiT(t, target)
	}
	resp, b := e.api(t, http.MethodPost, "/v1/jobs/"+e.jobID+"/previews", map[string]any{"port": port, "ttl_sec": 120})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create preview = %d %s", resp.StatusCode, b)
	}
	var out struct {
		ID       string `json:"id"`
		URL      string `json:"url"`
		Port     int    `json:"port"`
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.WorkerID != e2eWorkerID {
		t.Fatalf("worker_id = %q, want %q", out.WorkerID, e2eWorkerID)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatal(err)
	}
	l := previewLease{ID: out.ID, URL: out.URL, Base: u.Scheme + "://" + u.Host, Token: u.Query().Get("token"), Port: out.Port}
	r := previewGet(t, l.Base+"/?token="+l.Token, "")
	for _, c := range r.Cookies() {
		if c.Name == "gofer_preview" {
			l.Session = c.Value
		}
	}
	if l.Session == "" {
		t.Fatalf("token exchange = %d, no cookie", r.StatusCode)
	}
	return l
}

func atoiT(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

var previewNoRedirect = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func previewGet(t *testing.T, rawURL, session string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	if session != "" {
		req.Header.Set("Cookie", "gofer_preview="+session)
	}
	resp, err := previewNoRedirect.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func dialPreviewWS(t *testing.T, l previewLease) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(l.Base, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {"gofer_preview=" + l.Session}, "Origin": {l.Base}},
	})
	if err != nil {
		t.Fatalf("dial preview ws: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func echoWS(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	_, b, err := c.Read(ctx)
	if err != nil || string(b) != "echo:"+msg {
		t.Fatalf("echo = %q err=%v", b, err)
	}
}

func waitSocketCut(t *testing.T, c *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("preview socket was not cut (err=%v)", err)
	}
}

func (e *previewE2E) leaseState(t *testing.T, id string) (state, reason string) {
	t.Helper()
	resp, b := e.api(t, http.MethodGet, "/v1/jobs/"+e.jobID+"/previews", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list previews = %d %s", resp.StatusCode, b)
	}
	var out struct {
		Previews []struct {
			ID          string `json:"id"`
			State       string `json:"state"`
			CloseReason string `json:"close_reason"`
		} `json:"previews"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	for _, p := range out.Previews {
		if p.ID == id {
			return p.State, p.CloseReason
		}
	}
	t.Fatalf("lease %s not listed", id)
	return "", ""
}

// logSink captures every slog record of the process for the duration of a test.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *logSink) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.buf.String() }

func captureLogs(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return sink
}

func TestPreviewE2EHTTPAndWebSocket(t *testing.T) {
	logs := captureLogs(t)
	e := startPreviewE2E(t)
	l := e.openLease(t, e.app.port)

	// A page, through the worker.
	r := previewGet(t, l.Base+"/page?x=1", l.Session)
	if b, _ := io.ReadAll(r.Body); r.StatusCode != http.StatusOK || string(b) != "hello /page" {
		t.Fatalf("page = %d %q", r.StatusCode, b)
	}
	// A 302 is passed on untouched.
	r = previewGet(t, l.Base+"/redir", l.Session)
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/target" {
		t.Fatalf("redirect = %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// A binary body arrives byte for byte.
	r = previewGet(t, l.Base+"/bin", l.Session)
	got, err := io.ReadAll(r.Body)
	if err != nil || sha256.Sum256(got) != sha256.Sum256(previewBlob()) {
		t.Fatalf("binary body of %d bytes (err=%v) differs", len(got), err)
	}
	// Several resources at once.
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, l.Base+"/r", nil)
			req.Header.Set("Cookie", "gofer_preview="+l.Session)
			resp, err := previewNoRedirect.Do(req)
			if err != nil {
				errs <- err.Error()
				return
			}
			defer resp.Body.Close()
			if b, _ := io.ReadAll(resp.Body); string(b) != "hello /r" {
				errs <- string(b)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for m := range errs {
		t.Fatalf("concurrent fetch: %s", m)
	}
	// The cookie is required; the one-time token does not work twice.
	if r := previewGet(t, l.Base+"/", ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("no cookie = %d, want 404", r.StatusCode)
	}
	if r := previewGet(t, l.Base+"/?token="+l.Token, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("reused token = %d, want 404", r.StatusCode)
	}

	// WebSocket echo, and a foreign Origin is refused.
	c := dialPreviewWS(t, l)
	echoWS(t, c, "one")
	echoWS(t, c, "two")
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(l.Base, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {"gofer_preview=" + l.Session}, "Origin": {"http://evil.example"}},
	}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin err=%v resp=%v, want 403", err, resp)
	}

	// Revoking the lease cuts the open socket and, with it, the worker's relay to the app.
	if resp, b := e.api(t, http.MethodDelete, "/v1/jobs/"+e.jobID+"/previews/"+l.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d %s", resp.StatusCode, b)
	}
	waitSocketCut(t, c)
	wait.Until(t, 10*time.Second, "the app's socket to close", func() bool { return e.app.wsOpen.Load() == 0 })

	// No secret ever reached a log line or an audit row.
	e.assertNoSecret(t, logs.String(), l)
}

func (e *previewE2E) assertNoSecret(t *testing.T, logs string, l previewLease) {
	t.Helper()
	for what, secret := range map[string]string{"token": l.Token, "cookie": l.Session} {
		if secret == "" {
			t.Fatalf("missing %s", what)
		}
		if strings.Contains(logs, secret) {
			t.Errorf("the preview %s appears in the logs", what)
		}
		for _, kind := range []string{"preview_created", "preview_revoked", "preview_job_ended", "preview_worker_lost", "preview_auth_failed", "preview_expired"} {
			rows, err := e.hub.store.ListAuditEventsSince(kind, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if strings.Contains(row.Detail, secret) || strings.Contains(row.TargetID, secret) {
					t.Errorf("the preview %s appears in audit row %s", what, kind)
				}
			}
		}
	}
	if strings.Contains(logs, l.URL) {
		t.Error("the full preview URL appears in the logs")
	}
}

func TestPreviewE2EUnreachablePortIs502(t *testing.T) {
	e := startPreviewE2E(t)
	dead := startPreviewApp(t)
	port := dead.port
	dead.srv.Close()
	l := e.openLease(t, port)
	if r := previewGet(t, l.Base+"/", l.Session); r.StatusCode != http.StatusBadGateway {
		t.Fatalf("closed port = %d, want 502", r.StatusCode)
	}
}

func TestPreviewE2EJobEndClosesPreview(t *testing.T) {
	e := startPreviewE2E(t)
	l := e.openLease(t, e.app.port)
	c := dialPreviewWS(t, l)
	echoWS(t, c, "alive")
	wait.Until(t, 10*time.Second, "the app socket", func() bool { return e.app.wsOpen.Load() == 1 })

	if err := e.hub.jobs.Cancel(e.jobID); err != nil {
		t.Fatal(err)
	}
	e.hub.jobs.Wait(e.jobID)

	waitSocketCut(t, c)
	wait.Until(t, 10*time.Second, "the app's socket to close", func() bool { return e.app.wsOpen.Load() == 0 })
	wait.For(t, 10*time.Second, "lease closed as job_ended", func() (bool, any) {
		st, why := e.leaseState(t, l.ID)
		return st == "closed" && why == "job_ended", st + "/" + why
	})
	if r, err := previewNoRedirect.Get(l.Base + "/"); err == nil {
		defer r.Body.Close()
		if r.StatusCode == http.StatusOK {
			t.Fatal("the preview still answers after the job ended")
		}
	}
}

func TestPreviewE2EWorkerDisconnectClosesLease(t *testing.T) {
	e := startPreviewE2E(t)
	l := e.openLease(t, e.app.port)
	c := dialPreviewWS(t, l)
	echoWS(t, c, "alive")

	e.stopWk() // the worker process goes away

	waitSocketCut(t, c)
	wait.Until(t, 10*time.Second, "the app's socket to close", func() bool { return e.app.wsOpen.Load() == 0 })
	// Which of the two ends the lease first is a race (the lost worker also fails its job);
	// either way it is closed and never rebound.
	wait.For(t, 10*time.Second, "lease closed", func() (bool, any) {
		st, why := e.leaseState(t, l.ID)
		return st == "closed" && (why == "worker_lost" || why == "job_ended"), st + "/" + why
	})
}

// A worker below protocol v29 registers fine but cannot serve previews: 409 with the stable
// code and the version it needs, and nothing is allocated.
func TestPreviewE2EOldProtocolWorkerIs409(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 20*time.Second))
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(hub.ts.URL, "http") + "/v1/workers/connect"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + e2eToken}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	reg, _ := json.Marshal(wsproto.Register{WorkerID: e2eWorkerID, InstanceID: "old-inst", ProtocolVersion: wsproto.PreviewMinProtocolVersion - 1, MaxConcurrent: 1})
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegister, Payload: reg}); err != nil {
		t.Fatal(err)
	}
	waitWorkerOnline(t, hub.hub)
	if proto, _ := hub.hub.WorkerProtocol(e2eWorkerID); proto != wsproto.PreviewMinProtocolVersion-1 {
		t.Fatalf("hub sees protocol %d", proto)
	}
	go func() { // keep answering the hub's pings so the fake stays registered
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	now := time.Now().Unix()
	if err := hub.store.UpsertJob(jobstore.JobRecord{
		ID: "job-old", ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID, Status: job.StatusRunning,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	e := &previewE2E{hub: hub, jobID: "job-old"}
	resp, b := e.api(t, http.MethodPost, "/v1/jobs/job-old/previews", map[string]any{"port": 3000})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d %s, want 409", resp.StatusCode, b)
	}
	var body struct {
		Code   string         `json:"code"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "preview_worker_outdated" || body.Params["worker"] != e2eWorkerID || body.Params["version"] != float64(wsproto.PreviewMinProtocolVersion) {
		t.Fatalf("body = %s", b)
	}
	if vs, _ := hub.store.ListPreviews("job-old"); len(vs) != 0 {
		t.Fatalf("a refused create left leases: %+v", vs)
	}
}
