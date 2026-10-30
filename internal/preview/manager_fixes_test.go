package preview

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

func TestListJSONUsesSnakeCaseNames(t *testing.T) {
	b, err := jsonMarshal(Lease{ID: "p1", JobID: "j1", CallerID: "c", WorkerID: "local", Port: 9, State: "active", CreatedAt: 1, ExpiresAt: 2, CloseReason: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"id":"p1"`, `"job_id":"j1"`, `"caller_id":"c"`, `"worker_id":"local"`, `"port":9`, `"state":"active"`, `"created_at":1`, `"expires_at":2`, `"close_reason":"x"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("json %s lacks %s", b, k)
		}
	}
}

func TestTokenUsableByExactlyOneConcurrentRequest(t *testing.T) {
	m, _ := newManager(t, 1)
	_, base, token := createRaw(t, m, "job-1", okTargetPort(t), 60)
	var wins, notFound atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, base+"/?token="+token, nil)
			resp, err := noRedirect.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusSeeOther:
				wins.Add(1)
			case http.StatusNotFound:
				notFound.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || notFound.Load() != 7 {
		t.Fatalf("303s=%d 404s=%d, want exactly one winner", wins.Load(), notFound.Load())
	}
}

func TestMaxConnectionsPerLease(t *testing.T) {
	m, _ := newManager(t, 2)
	m.cfg.MaxConnections = 2
	_, base, session := create(t, m, "job-1", okTargetPort(t), 60)
	_, baseB, sessionB := create(t, m, "job-2", okTargetPort(t), 60)
	host := strings.TrimPrefix(base, "http://")

	var held []net.Conn
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	t.Cleanup(func() {
		for _, c := range held {
			_ = c.Close()
		}
	})
	cli := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	status := func(b, s string) int {
		req, _ := http.NewRequest(http.MethodGet, b+"/", nil)
		req.Header.Set("Cookie", "gofer_preview="+s)
		resp, err := cli.Do(req)
		if err != nil {
			return -1
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	wait.For(t, 10*time.Second, "third connection to be refused with 503", func() (bool, any) {
		got := status(base, session)
		return got == http.StatusServiceUnavailable, got
	})
	// Another lease has its own budget.
	if got := status(baseB, sessionB); got != http.StatusOK {
		t.Fatalf("other lease status=%d, want 200", got)
	}
	// Freeing a connection frees a slot.
	_ = held[0].Close()
	wait.For(t, 10*time.Second, "a slot to free after a connection closes", func() (bool, any) {
		got := status(base, session)
		return got == http.StatusOK, got
	})
}

func (m *Manager) liveOf(id string) *live {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lives[id]
}

// With a short real idle timeout the lease's connections are reclaimed, the lease is not.
func TestIdleReclaimsConnectionsButKeepsLease(t *testing.T) {
	m, _ := newManager(t, 1)
	m.idle = 300 * time.Millisecond
	l, base, session := create(t, m, "job-1", okTargetPort(t), 60)
	if resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session}); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	wait.For(t, 10*time.Second, "idle connections to be reclaimed", func() (bool, any) {
		n := m.liveOf(l.ID).openConns()
		return n == 0, n
	})
	if got, _, _ := m.Get(l.ID); got.State != "active" {
		t.Fatalf("state=%q, idle must not end the lease", got.State)
	}
	cli := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Cookie", "gofer_preview="+session)
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("reopen after idle: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reopen status=%d, want 200", resp.StatusCode)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeClock) add(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// The idle decision with an injected clock: activity and in-flight requests defer it.
func TestIdleDecisionUsesActivityAndInflight(t *testing.T) {
	m, _ := newManager(t, 1)
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	m.clock, m.idle = clk.now, time.Minute
	l, base, _ := create(t, m, "job-1", okTargetPort(t), 600)
	lv := m.liveOf(l.ID)

	c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	wait.For(t, 10*time.Second, "connection to be tracked", func() (bool, any) { n := lv.openConns(); return n >= 1, n })
	// Whatever the earlier requests left open is the baseline the checks compare against.
	n := lv.openConns()

	clk.add(30 * time.Second)
	lv.idleCheck()
	if got := lv.openConns(); got != n {
		t.Fatalf("conns=%d after 30s, want %d (not idle yet)", got, n)
	}
	lv.begin() // a request in flight
	clk.add(5 * time.Minute)
	lv.idleCheck()
	if got := lv.openConns(); got != n {
		t.Fatalf("conns=%d with a request in flight, want %d", got, n)
	}
	lv.end() // finishing counts as activity
	clk.add(30 * time.Second)
	lv.idleCheck()
	if got := lv.openConns(); got != n {
		t.Fatalf("conns=%d 30s after the last activity, want %d", got, n)
	}
	clk.add(31 * time.Second)
	lv.idleCheck()
	wait.For(t, 10*time.Second, "idle connections to be closed", func() (bool, any) { n := lv.openConns(); return n == 0, n })
	if got, _, _ := m.Get(l.ID); got.State != "active" {
		t.Fatalf("state=%q, want active", got.State)
	}
}

func auditRows(t *testing.T, st *jobstore.Store, kind string) []jobstore.AuditEventDetail {
	t.Helper()
	rows, err := st.ListAuditEventsSince(kind, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAuditEventsForLeaseLifecycle(t *testing.T) {
	m, _ := newManager(t, 4)
	m.authAuditEvery = 0
	tp := okTargetPort(t)

	a, baseA, tokA := createRaw(t, m, "job-a", tp, 60)
	// Refusals: wrong token, then the (now used) real one.
	get(t, baseA+"/?token=wrong", nil)
	exchangeToken(t, baseA, tokA)
	get(t, baseA+"/?token="+tokA, nil)
	if err := m.Revoke(a.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	b, _, _ := create(t, m, "job-b", tp, 60)
	m.CloseJob("job-b")
	c, _, _ := create(t, m, "job-c", tp, 1)
	wait.For(t, 10*time.Second, "lease c to expire", func() (bool, any) { g, _, _ := m.Get(c.ID); return g.State != "active", g.State })
	d, _, _ := create(t, m, "job-d", tp, 60)
	// A new process over the same store closes what this one left active.
	New(m.store, m.cfg)

	want := map[string]struct{ id, actor string }{
		"preview_created":        {a.ID, "caller-a"},
		"preview_revoked":        {a.ID, "alice"},
		"preview_job_ended":      {b.ID, "system"},
		"preview_expired":        {c.ID, "system"},
		"preview_server_restart": {d.ID, "system"},
	}
	for kind, w := range want {
		found := false
		for _, r := range auditRows(t, m.store, kind) {
			if r.TargetID == w.id {
				found = true
				if r.Actor != w.actor {
					t.Errorf("%s actor=%q, want %q", kind, r.Actor, w.actor)
				}
			}
		}
		if !found {
			t.Errorf("no %s audit row for %s", kind, w.id)
		}
	}
	denied := auditRows(t, m.store, "preview_auth_failed")
	reasons := ""
	for _, r := range denied {
		if r.TargetID == a.ID {
			reasons += r.Detail
		}
	}
	if !strings.Contains(reasons, "bad_token") || !strings.Contains(reasons, "token_used") {
		t.Fatalf("auth failure reasons=%s, want bad_token and token_used", reasons)
	}
	if len(denied) < 2 {
		t.Fatalf("auth failure rows=%d, want one per refusal", len(denied))
	}

	// No secret and no URL in any row.
	for _, kind := range []string{"preview_created", "preview_revoked", "preview_job_ended", "preview_expired", "preview_server_restart", "preview_auth_failed"} {
		for _, r := range auditRows(t, m.store, kind) {
			if strings.Contains(r.Detail, tokA) || strings.Contains(r.Detail, "token=") || strings.Contains(r.Detail, "http://") {
				t.Fatalf("audit detail leaks a secret or URL: %s", r.Detail)
			}
			if !strings.Contains(r.Detail, `"job_id"`) || !strings.Contains(r.Detail, `"preview_id"`) || !strings.Contains(r.Detail, `"port"`) {
				t.Fatalf("audit detail %s lacks job/preview/port", r.Detail)
			}
		}
	}
}

func TestAuthFailureAuditIsRateLimited(t *testing.T) {
	m, _ := newManager(t, 1)
	_, base, _ := createRaw(t, m, "job-1", okTargetPort(t), 60)
	for i := 0; i < 5; i++ {
		get(t, base+"/?token=nope", nil)
	}
	if n := len(auditRows(t, m.store, "preview_auth_failed")); n != 1 {
		t.Fatalf("auth failure rows=%d after 5 refusals within the interval, want 1", n)
	}
}

func wsTarget(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebSocketOriginMustBeThePreviewsOwn(t *testing.T) {
	m, _ := newManager(t, 1)
	_, base, session := create(t, m, "job-1", targetPort(t, wsTarget(t)), 60)
	dial := func(origin string) (int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
		defer cancel()
		h := http.Header{"Cookie": {"gofer_preview=" + session}}
		if origin != "" {
			h.Set("Origin", origin)
		}
		c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/ws", &websocket.DialOptions{HTTPHeader: h})
		if err == nil {
			_ = c.CloseNow()
			return http.StatusSwitchingProtocols, nil
		}
		if resp != nil {
			return resp.StatusCode, err
		}
		return 0, err
	}
	u, _ := url.Parse(base)
	_, port, _ := net.SplitHostPort(u.Host)
	for _, tc := range []struct {
		name, origin string
		want         int
	}{
		{"own origin", base, http.StatusSwitchingProtocols},
		{"no origin (not a browser)", "", http.StatusSwitchingProtocols},
		{"other site", "http://evil.example", http.StatusForbidden},
		{"same host other port", "http://" + u.Hostname() + ":1", http.StatusForbidden},
		{"same port other scheme", "https://" + u.Hostname() + ":" + port, http.StatusForbidden},
		{"null origin", "null", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dial(tc.origin)
			if got != tc.want {
				t.Fatalf("status=%d (err=%v), want %d", got, err, tc.want)
			}
		})
	}
	// A bad Origin is not a way to probe: without the cookie it is still a plain 404.
	req, _ := http.NewRequest(http.MethodGet, base+"/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Origin", "http://evil.example")
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cookieless upgrade status=%d, want 404", resp.StatusCode)
	}
}

func TestSetCookieWithDomainIsDropped(t *testing.T) {
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Add("Set-Cookie", "a=1; Path=/; Domain=example.test")
		h.Add("Set-Cookie", "b=2; domain=.example.test; Secure")
		h.Add("Set-Cookie", "c=3; Path=/; HttpOnly")
		h.Add("Set-Cookie", "gofer_d=4; Path=/")
		h.Add("Set-Cookie", "e=domain=x; Path=/")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(tg.Close)
	m, _ := newManager(t, 1)
	_, base, session := create(t, m, "job-1", targetPort(t, tg), 60)
	resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session})
	names := map[string]bool{}
	for _, c := range resp.Cookies() {
		names[c.Name] = true
	}
	if names["a"] || names["b"] || names["gofer_d"] || !names["c"] || !names["e"] || len(names) != 2 {
		t.Fatalf("forwarded cookies=%v, want only c and e", names)
	}
}

func TestRenewExtendsLeaseAndResetsTimer(t *testing.T) {
	m, _ := newManager(t, 1)
	m.cfg.MaxTTLSec = 100
	l, base, session := create(t, m, "job-1", okTargetPort(t), 2)

	r, err := m.Renew(l.ID, 3600, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if now := time.Now().Unix(); r.ExpiresAt > now+100 || r.ExpiresAt < now+95 {
		t.Fatalf("expires_at=%d (now %d), want about now+max_ttl(100)", r.ExpiresAt, now)
	}
	// Past the original expiry the old timer must not close the renewed lease.
	wait.Until(t, 10*time.Second, "original expiry to pass", func() bool { return time.Now().Unix() > l.ExpiresAt })
	if resp := get(t, base+"/", map[string]string{"Cookie": "gofer_preview=" + session}); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d after the original expiry, want the renewed lease to serve", resp.StatusCode)
	}
	if got, _, _ := m.Get(l.ID); got.State != "active" || got.ExpiresAt != r.ExpiresAt {
		t.Fatalf("lease=%+v, want active with the renewed expiry", got)
	}
	// ttl 0 takes the default, capped by max.
	if r2, err := m.Renew(l.ID, 0, "alice"); err != nil || r2.ExpiresAt > time.Now().Unix()+100 {
		t.Fatalf("default renew: %+v err=%v", r2, err)
	}
	rows := auditRows(t, m.store, "preview_renewed")
	if len(rows) != 2 || rows[0].Actor != "alice" {
		t.Fatalf("renew audit rows=%+v", rows)
	}
	// A closed lease cannot be revived.
	if err := m.Revoke(l.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Renew(l.ID, 60, "alice"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("renew after revoke err=%v, want ErrNotActive", err)
	}
	if _, err := m.Renew("no-such", 60, "alice"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("renew unknown err=%v, want ErrNotActive", err)
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
