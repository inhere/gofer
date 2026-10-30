// Package preview implements server-local job preview leases and loopback proxying.
package preview

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
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

type Lease struct {
	ID, JobID, CallerID, WorkerID string
	Port                          int
	State                         string
	CreatedAt, ExpiresAt          int64
	CloseReason                   string
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
	wg      sync.WaitGroup
}

func New(store *jobstore.Store, cfg config.PreviewConfig) *Manager {
	// Leases a previous serve left active have no listener in this process: close them
	// so they neither look alive nor keep their ports out of the pool.
	if store != nil {
		_ = store.CloseActivePreviews("server_restart", time.Now().Unix())
	}
	return &Manager{store: store, cfg: cfg.Effective(), listeners: map[string]net.Listener{}, servers: map[string]*http.Server{}, ports: map[int]bool{}, cancels: map[string]context.CancelFunc{}, timers: map[string]*time.Timer{}}
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
	m.listeners[id] = ln
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ModifyResponse = func(resp *http.Response) error {
		cookies := resp.Header.Values("Set-Cookie")
		resp.Header.Del("Set-Cookie")
		for _, v := range cookies {
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "gofer_") {
				resp.Header.Add("Set-Cookie", v)
			}
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
	h := m.handler(id, tok, rp)
	baseCtx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return baseCtx }}
	m.servers[id] = srv
	m.cancels[id] = cancel
	// The lease ends on its own at expiry, so the port returns to the pool without
	// anyone having to send a request to notice.
	m.timers[id] = time.AfterFunc(time.Duration(ttl)*time.Second, func() { _ = m.Close(id, "expired") })
	go func() { _ = srv.Serve(ln) }()
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	return Lease{ID: id, JobID: jobID, CallerID: caller, WorkerID: "local", Port: p, State: "active", CreatedAt: now, ExpiresAt: now + int64(ttl)}, fmt.Sprintf("http://%s:%d/?token=%s", host, p, tok), nil
}
func (m *Manager) handler(id, token string, rp *httputil.ReverseProxy) http.Handler {
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
		// The one-time token in the URL is traded for a cookie scoped to this preview's
		// origin, then the browser is sent to the same URL without it, so the token does
		// not stay in the address bar, the history or a bookmark. Compared in constant
		// time: the token is the only thing guarding the preview.
		if q := r.URL.Query(); q.Has("token") {
			if !sameToken(q.Get("token"), token) {
				http.NotFound(w, r)
				return
			}
			// Secure follows the transport: the default port mode serves plain HTTP on a LAN
			// address, where a Secure cookie would never be sent back and the preview would
			// break; behind TLS (a base_domain deployment) it is always Secure.
			http.SetCookie(w, &http.Cookie{Name: "gofer_preview", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(rec.ExpiresAt - time.Now().Unix())})
			q.Del("token")
			clean := *r.URL
			clean.RawQuery = q.Encode()
			http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
			return
		}
		if c, _ := r.Cookie("gofer_preview"); c == nil || !sameToken(c.Value, token) {
			http.NotFound(w, r)
			return
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
	now := time.Now().Unix()
	if t := m.timers[id]; t != nil {
		t.Stop()
		delete(m.timers, id)
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
	return m.store.UpdatePreviewState(id, "closed", reason, now)
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
