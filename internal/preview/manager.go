// Package preview implements server-local job preview leases and loopback proxying.
package preview

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// ErrNotActive is returned when an operation needs a live lease and the lease is gone.
var ErrNotActive = errors.New("preview lease is not active")

// Lease is the non-secret view of a preview lease. Port is the preview port the browser
// opens (not the job's target port).
type Lease struct {
	ID          string `json:"id"`
	JobID       string `json:"job_id"`
	CallerID    string `json:"caller_id"`
	WorkerID    string `json:"worker_id"`
	Port        int    `json:"port"`
	State       string `json:"state"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
	CloseReason string `json:"close_reason,omitempty"`
}
type Manager struct {
	store     *jobstore.Store
	cfg       config.PreviewConfig
	mu        sync.Mutex
	listeners map[string]net.Listener
	servers   map[string]*http.Server
	ports     map[int]bool
	// cancels end the request contexts of one lease's server, which is what cuts the
	// connections net/http no longer tracks once they are hijacked (WebSocket upgrades).
	cancels map[string]context.CancelFunc
	timers  map[string]*time.Timer
	lives   map[string]*live
	wg      sync.WaitGroup

	// idle is how long a lease may see no traffic before its connections are reclaimed
	// (0 = never). clock feeds the idle bookkeeping and authAuditEvery rate-limits the
	// audit rows for refused requests; tests replace all three before creating a lease.
	idle           time.Duration
	clock          func() time.Time
	authAuditEvery time.Duration
}

func New(store *jobstore.Store, cfg config.PreviewConfig) *Manager {
	// Leases a previous serve left active have no listener in this process: close them
	// so they neither look alive nor keep their ports out of the pool.
	m := &Manager{store: store, cfg: cfg.Effective(), listeners: map[string]net.Listener{}, servers: map[string]*http.Server{}, ports: map[int]bool{}, cancels: map[string]context.CancelFunc{}, timers: map[string]*time.Timer{}, lives: map[string]*live{}, clock: time.Now, authAuditEvery: 10 * time.Second}
	m.idle = time.Duration(m.cfg.IdleTimeoutSec) * time.Second
	if store != nil {
		stale, _ := store.ListActivePreviews()
		_ = store.CloseActivePreviews("server_restart", time.Now().Unix())
		for _, p := range stale {
			m.audit("preview_server_restart", p, "system", "server_restart")
		}
	}
	return m
}

// audit appends one audit row for a lease: ids, caller, worker, port and reason, never the
// token, a cookie or the URL.
func (m *Manager) audit(kind string, p jobstore.PreviewRecord, actor, reason string) {
	if m.store == nil {
		return
	}
	d, _ := json.Marshal(map[string]any{"job_id": p.JobID, "preview_id": p.ID, "caller_id": p.CallerID, "worker_id": p.WorkerID, "port": p.Port, "state": p.State, "reason": reason})
	_, _ = m.store.AppendAuditEvent(kind, p.ID, actor, string(d))
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func randomID() (string, error) {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func parseRange(v string) (int, int, error) {
	var a, b int
	if _, e := fmt.Sscanf(strings.TrimSpace(v), "%d-%d", &a, &b); e != nil || a < 1 || b > 65535 || a > b {
		return 0, 0, errors.New("invalid preview port range")
	}
	return a, b, nil
}
func (m *Manager) allocPort() (int, error) {
	a, b, e := parseRange(m.cfg.PortRange)
	if e != nil {
		return 0, e
	}
	for p := a; p <= b; p++ {
		if m.ports[p] {
			continue
		}
		ln, e := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if e == nil {
			ln.Close()
			m.ports[p] = true
			return p, nil
		}
	}
	return 0, errors.New("preview port pool exhausted")
}
func (m *Manager) Create(ctx context.Context, jobID, caller, host string, port, ttl int, jobRunning bool) (Lease, string, error) {
	if !m.cfg.Enabled {
		return Lease{}, "", errors.New("job preview is disabled")
	}
	if !jobRunning {
		return Lease{}, "", errors.New("job is not running")
	}
	if port < 1 || port > 65535 {
		return Lease{}, "", errors.New("port must be between 1 and 65535")
	}
	if ttl <= 0 {
		ttl = m.cfg.DefaultTTLSec
	}
	if ttl > m.cfg.MaxTTLSec {
		ttl = m.cfg.MaxTTLSec
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil {
		return Lease{}, "", errors.New("preview store unavailable")
	}
	p, e := m.allocPort()
	if e != nil {
		return Lease{}, "", e
	}
	id, e := randomID()
	if e != nil {
		return Lease{}, "", e
	}
	tok, e := randomToken()
	if e != nil {
		return Lease{}, "", e
	}
	now := time.Now().Unix()
	rec := jobstore.PreviewRecord{ID: id, TokenHash: jobstore.PreviewTokenHash(tok), JobID: jobID, CallerID: caller, WorkerID: "local", WorkerInstanceID: "local", Port: p, State: "active", CreatedAt: now, ExpiresAt: now + int64(ttl)}
	if e = m.store.InsertPreview(rec); e != nil {
		delete(m.ports, p)
		return Lease{}, "", e
	}
	ln, e := net.Listen("tcp", fmt.Sprintf(":%d", p))
	if e != nil {
		_ = m.store.UpdatePreviewState(id, "closed", "listen_failed", time.Now().Unix())
		delete(m.ports, p)
		return Lease{}, "", e
	}
	lv := newLive(m, id, m.cfg.MaxConnections)
	m.lives[id] = lv
	m.listeners[id] = ln
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ModifyResponse = func(resp *http.Response) error {
		cookies := resp.Header.Values("Set-Cookie")
		resp.Header.Del("Set-Cookie")
		for _, v := range cookies {
			// A gofer_ cookie would shadow the console's own; a Domain attribute would let
			// the preview app plant a cookie for the parent domain, shared with the console.
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "gofer_") || cookieHasDomain(v) {
				continue
			}
			resp.Header.Add("Set-Cookie", v)
		}
		return nil
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "preview target unavailable", http.StatusBadGateway)
	}
	rp.Director = func(r *http.Request) {
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		r.Host = target.Host
		r.Header.Del("Authorization")
		r.Header.Set("Cookie", stripPreviewCookie(r.Header.Get("Cookie")))
	}
	h := m.handler(id, tok, rp, lv)
	baseCtx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return baseCtx }}
	m.servers[id] = srv
	m.cancels[id] = cancel
	// The lease ends on its own at expiry, so the port returns to the pool without
	// anyone having to send a request to notice.
	m.armExpiry(id, time.Duration(ttl)*time.Second)
	go func() { _ = srv.Serve(&limitListener{Listener: ln, lv: lv}) }()
	m.audit("preview_created", rec, caller, "created")
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	return Lease{ID: id, JobID: jobID, CallerID: caller, WorkerID: "local", Port: p, State: "active", CreatedAt: now, ExpiresAt: now + int64(ttl)}, fmt.Sprintf("http://%s:%d/?token=%s", host, p, tok), nil
}

// armExpiry (re)starts the lease's expiry timer. Caller holds m.mu.
func (m *Manager) armExpiry(id string, d time.Duration) {
	if t := m.timers[id]; t != nil {
		t.Stop()
	}
	m.timers[id] = time.AfterFunc(d, func() { m.expireDue(id) })
}

// expireDue closes the lease if it is really due: a renew may have moved the expiry after
// this timer was already running, in which case the renewed timer owns the lease.
func (m *Manager) expireDue(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok, e := m.store.GetPreview(id); e == nil && ok && p.State == "active" && p.ExpiresAt > time.Now().Unix() {
		return
	}
	_ = m.closeLocked(id, "expired", "system")
}

// cookieHasDomain reports whether a Set-Cookie value carries a Domain attribute.
func cookieHasDomain(v string) bool {
	parts := strings.Split(v, ";")
	for _, a := range parts[1:] {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "domain" || strings.HasPrefix(a, "domain=") {
			return true
		}
	}
	return false
}

// originAllowed applies to WebSocket upgrades: a browser always sends Origin there, and it
// must be this preview's own origin (scheme + host + preview port), or another site could
// drive the socket with the visitor's cookie. A client that sends no Origin is not a
// browser and still needs the cookie.
func originAllowed(r *http.Request, port int) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.Port() == strconv.Itoa(port)
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (m *Manager) handler(id, token string, rp *httputil.ReverseProxy, lv *live) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ok, e := m.store.GetPreview(id)
		if e == nil && ok && rec.State == "active" && rec.ExpiresAt <= time.Now().Unix() {
			// Not synchronously: Close shuts this very server down, and a handler
			// waiting for its own server to drain never returns.
			m.wg.Add(1)
			go func() { defer m.wg.Done(); _ = m.Close(id, "expired") }()
		}
		if e != nil || !ok || rec.State != "active" || rec.ExpiresAt <= time.Now().Unix() {
			http.NotFound(w, r)
			return
		}
		// The one-time token in the URL is traded, once, for a random cookie scoped to this preview's
		// origin (the token itself is never the cookie, and stops working after that), then the browser is sent to the same URL without it, so the token does
		// not stay in the address bar, the history or a bookmark. Compared in constant
		// time: the token is the only thing guarding the preview.
		deny := func(reason string) {
			if lv.shouldAuditDenied() {
				m.audit("preview_auth_failed", rec, "anonymous", reason)
			}
			http.NotFound(w, r)
		}
		if q := r.URL.Query(); q.Has("token") {
			session, reason, err := lv.exchange(q.Get("token"), token)
			if err != nil {
				http.Error(w, "preview unavailable", http.StatusInternalServerError)
				return
			}
			if reason != "" {
				deny(reason)
				return
			}
			// Secure follows the transport: the default port mode serves plain HTTP on a LAN
			// address, where a Secure cookie would never be sent back and the preview would
			// break; behind TLS (a base_domain deployment) it is always Secure.
			http.SetCookie(w, &http.Cookie{Name: "gofer_preview", Value: session, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(rec.ExpiresAt - time.Now().Unix())})
			q.Del("token")
			clean := *r.URL
			clean.RawQuery = q.Encode()
			http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
			return
		}
		c, _ := r.Cookie("gofer_preview")
		if c == nil {
			deny("no_cookie")
			return
		}
		if !lv.validSession(c.Value) {
			deny("bad_cookie")
			return
		}
		upgrade := isUpgrade(r)
		if upgrade && !originAllowed(r, rec.Port) {
			if lv.shouldAuditDenied() {
				m.audit("preview_auth_failed", rec, "anonymous", "bad_origin")
			}
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		// A WebSocket stays inside this handler for as long as it lives; it is accounted
		// by its connection's traffic instead, or it would never look idle.
		if !upgrade {
			lv.begin()
			defer lv.end()
		}
		r.Header.Del("Authorization")
		r.Header.Set("Cookie", stripPreviewCookie(r.Header.Get("Cookie")))
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		rp.ServeHTTP(w, r)
	})
}

// sameToken compares a presented preview token with the lease's in constant time.
func sameToken(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func stripPreviewCookie(raw string) string {
	var keep []string
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part != "" && !strings.HasPrefix(strings.ToLower(part), "gofer_preview=") {
			keep = append(keep, part)
		}
	}
	return strings.Join(keep, "; ")
}

func (m *Manager) Get(id string) (Lease, bool, error) {
	p, ok, err := m.store.GetPreview(id)
	if err != nil || !ok {
		return Lease{}, ok, err
	}
	return Lease{ID: p.ID, JobID: p.JobID, CallerID: p.CallerID, WorkerID: p.WorkerID, Port: p.Port, State: p.State, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt, CloseReason: p.CloseReason}, true, nil
}

func (m *Manager) List(jobID string) ([]Lease, error) {
	rs, e := m.store.ListPreviews(jobID)
	if e != nil {
		return nil, e
	}
	out := make([]Lease, 0, len(rs))
	for _, p := range rs {
		out = append(out, Lease{ID: p.ID, JobID: p.JobID, CallerID: p.CallerID, WorkerID: p.WorkerID, Port: p.Port, State: p.State, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt, CloseReason: p.CloseReason})
	}
	return out, nil
}
func (m *Manager) Close(id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeLocked(id, reason, "system")
}

// Revoke closes a lease on behalf of a caller; the audit row names who did it.
func (m *Manager) Revoke(id, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeLocked(id, "revoked", actor)
}

// closeLocked ends a lease. Caller holds m.mu.
func (m *Manager) closeLocked(id, reason, actor string) error {
	now := time.Now().Unix()
	if t := m.timers[id]; t != nil {
		t.Stop()
		delete(m.timers, id)
	}
	if lv := m.lives[id]; lv != nil {
		lv.stop()
		delete(m.lives, id)
	}
	if cancel := m.cancels[id]; cancel != nil {
		cancel()
		delete(m.cancels, id)
	}
	// Close, not Shutdown: a revoked or expired preview must not wait for its open
	// connections to go idle (and a handler may be what called us).
	srv := m.servers[id]
	live := srv != nil
	if srv != nil {
		_ = srv.Close()
		delete(m.servers, id)
	}
	if ln := m.listeners[id]; ln != nil {
		_ = ln.Close()
		delete(m.listeners, id)
	}
	p, ok, e := m.store.GetPreview(id)
	if e == nil && ok {
		if live {
			delete(m.ports, p.Port)
		}
		if p.State != "active" {
			// Already ended (expired, revoked): keep its recorded reason, and do not
			// touch the port, which may belong to another lease by now.
			return nil
		}
	}
	if err := m.store.UpdatePreviewState(id, "closed", reason, now); err != nil {
		return err
	}
	if ok {
		p.State = "closed"
		m.audit("preview_"+reason, p, actor, reason)
	}
	return nil
}

// Renew moves an active lease's expiry to now + ttl (ttl defaults and is capped like on
// create, so a lease never runs longer than max_ttl_sec from the renewal) and restarts its
// timer.
func (m *Manager) Renew(id string, ttl int, actor string) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok, e := m.store.GetPreview(id)
	if e != nil {
		return Lease{}, e
	}
	if !ok || p.State != "active" || m.servers[id] == nil || p.ExpiresAt <= time.Now().Unix() {
		return Lease{}, ErrNotActive
	}
	if ttl <= 0 {
		ttl = m.cfg.DefaultTTLSec
	}
	if ttl > m.cfg.MaxTTLSec {
		ttl = m.cfg.MaxTTLSec
	}
	exp := time.Now().Unix() + int64(ttl)
	if done, e := m.store.UpdatePreviewExpiry(id, exp); e != nil {
		return Lease{}, e
	} else if !done {
		return Lease{}, ErrNotActive
	}
	m.armExpiry(id, time.Duration(ttl)*time.Second)
	p.ExpiresAt = exp
	m.audit("preview_renewed", p, actor, "renewed")
	return Lease{ID: p.ID, JobID: p.JobID, CallerID: p.CallerID, WorkerID: p.WorkerID, Port: p.Port, State: p.State, CreatedAt: p.CreatedAt, ExpiresAt: exp}, nil
}
func (m *Manager) CloseJob(jobID string) {
	rs, e := m.store.ListPreviews(jobID)
	if e != nil {
		return
	}
	for _, p := range rs {
		if p.State == "active" {
			_ = m.Close(p.ID, "job_ended")
		}
	}
	_ = m.store.ClosePreviewsForJob(jobID, "job_ended", time.Now().Unix())
}
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.servers))
	for id := range m.servers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Close(id, "shutdown")
	}
	m.wg.Wait()
}
func (m *Manager) Sweep()         { rs, e := m.store.ListPreviews(""); _ = rs; _ = e }
func (m *Manager) String() string { return strconv.Itoa(len(m.servers)) }
