package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// freeRange finds n consecutive loopback-free ports and returns them as a "lo-hi" pool range.
func freeRange(t *testing.T, n int) (string, []int) {
	t.Helper()
	for base := 24000; base < 60000; base += 37 {
		var lns []net.Listener
		ok := true
		for p := base; p < base+n; p++ {
			ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
			if err != nil {
				ok = false
				break
			}
			lns = append(lns, ln)
		}
		for _, ln := range lns {
			_ = ln.Close()
		}
		if ok {
			ports := make([]int, n)
			for i := range ports {
				ports[i] = base + i
			}
			return fmt.Sprintf("%d-%d", base, base+n-1), ports
		}
	}
	t.Fatalf("no %d consecutive free ports", n)
	return "", nil
}

// newManager builds a Manager over a temp store with a pool of n free ports. The manager is
// closed before the store (cleanups run last-in first-out).
func newManager(t *testing.T, n int) (*Manager, []int) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rng, ports := freeRange(t, n)
	m := New(st, config.PreviewConfig{Enabled: true, PortRange: rng})
	t.Cleanup(m.CloseAll)
	return m, ports
}

func targetPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// create opens a lease for the given target port and splits the returned URL.
func create(t *testing.T, m *Manager, jobID string, target int, ttl int) (lease Lease, base, token string) {
	t.Helper()
	l, raw, err := m.Create(context.Background(), jobID, "caller-a", "127.0.0.1", target, ttl, true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return l, u.Scheme + "://" + u.Host, u.Query().Get("token")
}

var noRedirect = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func get(t *testing.T, rawURL string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func okTarget(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPortPoolAllocExhaustAndReuse(t *testing.T) {
	m, ports := newManager(t, 2)
	tg := okTarget(t)
	tp := targetPort(t, tg)

	a, _, _ := create(t, m, "job-1", tp, 60)
	b, _, _ := create(t, m, "job-1", tp, 60)
	got := map[int]bool{a.Port: true, b.Port: true}
	if len(got) != 2 || !got[ports[0]] || !got[ports[1]] {
		t.Fatalf("leases got ports %d,%d, want the two pool ports %v", a.Port, b.Port, ports)
	}

	if _, _, err := m.Create(context.Background(), "job-1", "caller-a", "", tp, 60, true); err == nil ||
		!strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("third create err=%v, want pool exhausted", err)
	}

	if err := m.Close(a.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	c, _, _ := create(t, m, "job-1", tp, 60)
	if c.Port != a.Port {
		t.Fatalf("recycled port=%d, want the closed lease's %d", c.Port, a.Port)
	}
}

func TestCreateValidation(t *testing.T) {
	m, _ := newManager(t, 1)
	ctx := context.Background()
	if _, _, err := m.Create(ctx, "job-1", "c", "", 0, 60, true); err == nil {
		t.Fatal("port 0 must be rejected")
	}
	if _, _, err := m.Create(ctx, "job-1", "c", "", 8080, 60, false); err == nil {
		t.Fatal("a job that is not running must be rejected")
	}
	off := New(m.store, config.PreviewConfig{})
	if _, _, err := off.Create(ctx, "job-1", "c", "", 8080, 60, true); err == nil {
		t.Fatal("a disabled manager must refuse to create")
	}
}

func TestTTLIsClampedToMax(t *testing.T) {
	m, _ := newManager(t, 1)
	m.cfg.MaxTTLSec = 100
	l, _, _ := create(t, m, "job-1", okTargetPort(t), 100000)
	if got := l.ExpiresAt - l.CreatedAt; got != 100 {
		t.Fatalf("ttl=%ds, want clamped to 100", got)
	}
}

func okTargetPort(t *testing.T) int { return targetPort(t, okTarget(t)) }

func TestAuth(t *testing.T) {
	m, _ := newManager(t, 1)
	_, base, token := create(t, m, "job-1", targetPort(t, okTarget(t)), 60)

	cases := []struct {
		name string
		path string
		hdr  map[string]string
	}{
		{"no token no cookie", "/", nil},
		{"wrong token", "/?token=nope", nil},
		{"empty token", "/?token=", nil},
		{"wrong cookie", "/", map[string]string{"Cookie": "gofer_preview=nope"}},
		{"token as cookie name only", "/", map[string]string{"Cookie": "other=" + token}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := get(t, base+tc.path, tc.hdr)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status=%d, want 404", resp.StatusCode)
			}
			if len(resp.Cookies()) != 0 {
				t.Fatalf("a refused request must not set cookies, got %v", resp.Cookies())
			}
		})
	}

	t.Run("right token redirects and sets cookie", func(t *testing.T) {
		resp := get(t, base+"/app/page?token="+token+"&a=1&b=x%20y", nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status=%d, want 303", resp.StatusCode)
		}
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if loc.Path != "/app/page" || loc.Query().Has("token") || loc.Query().Get("a") != "1" || loc.Query().Get("b") != "x y" {
			t.Fatalf("Location=%q, want same path with the other params and no token", resp.Header.Get("Location"))
		}
		if strings.Contains(resp.Header.Get("Location"), token) {
			t.Fatal("token leaked into Location")
		}
		var ck *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == "gofer_preview" {
				ck = c
			}
		}
		if ck == nil {
			t.Fatal("no gofer_preview cookie")
		}
		if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.Value != token || ck.MaxAge <= 0 {
			t.Fatalf("cookie=%+v, want HttpOnly, SameSite=Strict, Path=/, token value, positive MaxAge", ck)
		}
	})

	t.Run("right cookie is forwarded", func(t *testing.T) {
		resp := get(t, base+"/x", map[string]string{"Cookie": "gofer_preview=" + token})
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(b) != "hello /x" {
			t.Fatalf("status=%d body=%q, want the target's answer", resp.StatusCode, b)
		}
	})
}

func TestForwardingStripsAndFilters(t *testing.T) {
	type seen struct{ auth, cookie, host string }
	ch := make(chan seen, 1)
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch <- seen{r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Host}
		http.SetCookie(w, &http.Cookie{Name: "gofer_x", Value: "1"})
		http.SetCookie(w, &http.Cookie{Name: "GOFER_session", Value: "2"})
		http.SetCookie(w, &http.Cookie{Name: "app", Value: "3"})
		http.SetCookie(w, &http.Cookie{Name: "mygofer_y", Value: "4"})
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(tg.Close)
	m, _ := newManager(t, 1)
	_, base, token := create(t, m, "job-1", targetPort(t, tg), 60)

	resp := get(t, base+"/", map[string]string{
		"Authorization": "Bearer console-secret",
		"Cookie":        "a=1; gofer_preview=" + token + "; b=2",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	s := <-ch
	if s.auth != "" {
		t.Fatalf("target saw Authorization %q", s.auth)
	}
	if strings.Contains(s.cookie, "gofer_preview") || strings.Contains(s.cookie, token) || s.cookie != "a=1; b=2" {
		t.Fatalf("target Cookie=%q, want only the application's cookies", s.cookie)
	}
	if s.host != fmt.Sprintf("127.0.0.1:%d", targetPort(t, tg)) {
		t.Fatalf("target Host=%q", s.host)
	}

	names := map[string]bool{}
	for _, c := range resp.Cookies() {
		names[c.Name] = true
	}
	if names["gofer_x"] || names["GOFER_session"] {
		t.Fatalf("gofer_* Set-Cookie leaked through: %v", names)
	}
	if !names["app"] || !names["mygofer_y"] {
		t.Fatalf("other Set-Cookie headers must be kept, got %v", names)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy=%q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestUnreachableTargetIs502(t *testing.T) {
	// A port that was just released: nothing listens there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	m, _ := newManager(t, 1)
	_, base, token := create(t, m, "job-1", dead, 60)
	resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + token})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", resp.StatusCode)
	}
}

func echoTarget(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if err := c.Write(ctx, typ, append([]byte("echo:"), b...)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dialWS(t *testing.T, base, token string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {"gofer_preview=" + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func TestWebSocketThroughProxy(t *testing.T) {
	m, _ := newManager(t, 1)
	_, base, token := create(t, m, "job-1", targetPort(t, echoTarget(t)), 60)

	c := dialWS(t, base, token)
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	for _, msg := range []string{"one", "two"} {
		if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		_, b, err := c.Read(ctx)
		if err != nil || string(b) != "echo:"+msg {
			t.Fatalf("read=%q err=%v, want echo", b, err)
		}
	}

	// Without the cookie the upgrade is refused.
	dctx, dcancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer dcancel()
	if _, resp, err := websocket.Dial(dctx, "ws"+strings.TrimPrefix(base, "http")+"/ws", nil); err == nil {
		t.Fatal("upgrade without cookie must fail")
	} else if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resp=%v err=%v, want 404", resp, err)
	}
}

func TestCloseCutsOpenWebSocket(t *testing.T) {
	m, _ := newManager(t, 1)
	l, base, token := create(t, m, "job-1", targetPort(t, echoTarget(t)), 60)
	c := dialWS(t, base, token)
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}

	if err := m.Close(l.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	// A revoked preview must not leave an established tunnel to the application open.
	if _, _, err := c.Read(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read after close err=%v, want the connection to be cut", err)
	}
}

func TestExplicitCloseRefusesFurtherRequests(t *testing.T) {
	m, _ := newManager(t, 1)
	l, base, token := create(t, m, "job-1", okTargetPort(t), 60)
	if err := m.Close(l.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.Get(l.ID)
	if err != nil || !ok || got.State != "closed" || got.CloseReason != "revoked" {
		t.Fatalf("lease=%+v ok=%v err=%v, want closed/revoked", got, ok, err)
	}
	// The listener is gone: connecting fails (or, if a connection raced in, it is a 404).
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Cookie", "gofer_preview="+token)
	if resp, err := noRedirect.Do(req); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status=%d after close, want 404", resp.StatusCode)
		}
	}
}

func TestLeaseExpiry(t *testing.T) {
	m, _ := newManager(t, 1)
	l, base, token := create(t, m, "job-1", okTargetPort(t), 1)

	// The lease is valid while it lasts.
	if resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + token}); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d before expiry", resp.StatusCode)
	}

	// After the TTL the lease is closed by the manager itself: no request is needed to
	// notice, so the port goes back to the pool.
	wait.For(t, 10*time.Second, "lease to be closed after its ttl", func() (bool, any) {
		got, _, _ := m.Get(l.ID)
		return got.State != "active", got.State
	})
	got, _, _ := m.Get(l.ID)
	if got.State != "closed" && got.State != "expired" {
		t.Fatalf("state=%q, want closed or expired", got.State)
	}
	if got.CloseReason != "expired" {
		t.Fatalf("reason=%q, want expired", got.CloseReason)
	}
	again, _, _ := create(t, m, "job-2", okTargetPort(t), 60)
	if again.Port != l.Port {
		t.Fatalf("port %d was not recycled after expiry (got %d)", l.Port, again.Port)
	}
}

func TestExpiredLeaseRequestIs404(t *testing.T) {
	m, _ := newManager(t, 1)
	l, base, token := create(t, m, "job-1", okTargetPort(t), 1)
	// Wait for the lease's wall-clock expiry, then hit it straight away: whether the
	// manager's own timer or this request notices first, the answer is a 404 (not a hang).
	wait.Until(t, 10*time.Second, "lease expiry time to pass", func() bool { return time.Now().Unix() >= l.ExpiresAt })
	done := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
		req.Header.Set("Cookie", "gofer_preview="+token)
		resp, err := noRedirect.Do(req)
		if err != nil {
			// The listener may already be closed by the expiry timer.
			resp = nil
		}
		done <- resp
	}()
	select {
	case resp := <-done:
		if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status=%d, want 404", resp.StatusCode)
			}
		}
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("request to an expired lease hung")
	}
	wait.For(t, 10*time.Second, "expired lease to be closed", func() (bool, any) {
		got, _, _ := m.Get(l.ID)
		return got.State != "active", got.State
	})
}

func TestCloseJobClosesItsLeasesOnly(t *testing.T) {
	m, _ := newManager(t, 2)
	tp := okTargetPort(t)
	a, _, _ := create(t, m, "job-a", tp, 60)
	b, baseB, tokB := create(t, m, "job-b", tp, 60)

	m.CloseJob("job-a")

	ga, _, _ := m.Get(a.ID)
	if ga.State == "active" || ga.CloseReason != "job_ended" {
		t.Fatalf("job-a lease=%+v, want closed with job_ended", ga)
	}
	gb, _, _ := m.Get(b.ID)
	if gb.State != "active" {
		t.Fatalf("job-b lease=%+v, want untouched", gb)
	}
	if resp := get(t, baseB+"/", map[string]string{"Cookie": "gofer_preview=" + tokB}); resp.StatusCode != http.StatusOK {
		t.Fatalf("job-b preview status=%d", resp.StatusCode)
	}
	c, _, _ := create(t, m, "job-c", tp, 60)
	if c.Port != a.Port {
		t.Fatalf("job-a's port %d was not recycled (got %d)", a.Port, c.Port)
	}
}

func TestListScopedToJob(t *testing.T) {
	m, _ := newManager(t, 2)
	tp := okTargetPort(t)
	create(t, m, "job-a", tp, 60)
	create(t, m, "job-b", tp, 60)
	vs, err := m.List("job-a")
	if err != nil || len(vs) != 1 || vs[0].JobID != "job-a" {
		t.Fatalf("list=%+v err=%v, want only job-a's lease", vs, err)
	}
}

func TestCloseAllReleasesListeners(t *testing.T) {
	m, ports := newManager(t, 1)
	create(t, m, "job-1", okTargetPort(t), 60)
	m.CloseAll()
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", ports[0]))
	if err != nil {
		t.Fatalf("port still held after CloseAll: %v", err)
	}
	_ = ln.Close()
}

// A second Close of an ended lease (a revoke after the expiry timer, say) must neither
// rewrite its recorded reason nor free the port, which another lease may hold by now.
func TestCloseTwiceKeepsReasonAndPort(t *testing.T) {
	m, _ := newManager(t, 1)
	tp := okTargetPort(t)
	a, _, _ := create(t, m, "job-1", tp, 60)
	if err := m.Close(a.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	b, _, _ := create(t, m, "job-1", tp, 60)
	if b.Port != a.Port {
		t.Fatalf("port %d not recycled (got %d)", a.Port, b.Port)
	}
	if err := m.Close(a.ID, "expired"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := m.Get(a.ID); got.CloseReason != "revoked" {
		t.Fatalf("reason=%q, want the first close's", got.CloseReason)
	}
	if _, _, err := m.Create(context.Background(), "job-1", "c", "", tp, 60, true); err == nil {
		t.Fatal("the second close freed the port of the live lease: pool allocated it twice")
	}
	if got, _, _ := m.Get(b.ID); got.State != "active" {
		t.Fatalf("live lease state=%q", got.State)
	}
}

// A lease the previous serve left active has no listener in the new process: starting a
// manager over the same store closes it (reason server_restart) instead of leaving it
// looking alive.
func TestNewClosesLeasesLeftActiveByAPreviousProcess(t *testing.T) {
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().Unix()
	if err := st.InsertPreview(jobstore.PreviewRecord{
		ID: "pv-left-over", JobID: "job-1", CallerID: "caller-a", WorkerID: "local",
		TokenHash: jobstore.PreviewTokenHash("tok"), Port: 1, State: "active",
		CreatedAt: now, ExpiresAt: now + 3600,
	}); err != nil {
		t.Fatalf("insert lease: %v", err)
	}
	rng, _ := freeRange(t, 1)
	m := New(st, config.PreviewConfig{Enabled: true, PortRange: rng})
	t.Cleanup(m.CloseAll)
	rec, ok, err := st.GetPreview("pv-left-over")
	if err != nil || !ok {
		t.Fatalf("get lease: ok=%v err=%v", ok, err)
	}
	if rec.State != "closed" || rec.CloseReason != "server_restart" {
		t.Fatalf("lease state=%q reason=%q, want closed / server_restart", rec.State, rec.CloseReason)
	}
}
