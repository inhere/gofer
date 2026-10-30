package preview

import (
	"net"
	"sync"
	"time"
)

// live is the in-memory state of one active lease: its one-time token exchange, its open
// connections and its idle bookkeeping. It does not outlive the process: a restart closes
// every lease (see New), so nothing here needs to be persisted.
type live struct {
	m   *Manager
	id  string
	max int

	mu         sync.Mutex
	tokenUsed  bool
	session    string // cookie value minted by the single token exchange
	conns      map[*trackedConn]struct{}
	inflight   int
	lastActive time.Time
	idleTimer  *time.Timer
	closed     bool
	lastDenied time.Time
}

func newLive(m *Manager, id string, max int) *live {
	return &live{m: m, id: id, max: max, conns: map[*trackedConn]struct{}{}, lastActive: m.clock()}
}

// exchange trades the one-time URL token for the cookie value. The token works exactly
// once: whoever presents it first (and correctly) gets the session, everyone after that
// is refused, so a URL that leaked after use is worthless. reason names the refusal.
func (lv *live) exchange(got, want string) (session, reason string, err error) {
	lv.mu.Lock()
	defer lv.mu.Unlock()
	if !sameToken(got, want) {
		return "", "bad_token", nil
	}
	if lv.tokenUsed {
		return "", "token_used", nil
	}
	s, err := randomToken()
	if err != nil {
		return "", "", err
	}
	lv.tokenUsed, lv.session = true, s
	return s, "", nil
}

func (lv *live) validSession(v string) bool {
	lv.mu.Lock()
	defer lv.mu.Unlock()
	return lv.session != "" && sameToken(v, lv.session)
}

// touch records activity and makes sure an idle check is pending. Caller holds no lock.
func (lv *live) touch() {
	lv.mu.Lock()
	defer lv.mu.Unlock()
	lv.touchLocked()
}

func (lv *live) touchLocked() {
	lv.lastActive = lv.m.clock()
	if lv.idleTimer == nil && !lv.closed && lv.m.idle > 0 {
		lv.idleTimer = time.AfterFunc(lv.m.idle, lv.idleCheck)
	}
}

func (lv *live) begin() {
	lv.mu.Lock()
	lv.inflight++
	lv.touchLocked()
	lv.mu.Unlock()
}

func (lv *live) end() {
	lv.mu.Lock()
	lv.inflight--
	lv.touchLocked()
	lv.mu.Unlock()
}

// idleCheck reclaims the lease's connections once nothing has happened on it for the idle
// timeout: no request in flight and no bytes moved on any connection. The lease itself
// stays valid until it expires; the user can simply open the preview again.
func (lv *live) idleCheck() {
	lv.mu.Lock()
	if lv.closed {
		lv.mu.Unlock()
		return
	}
	idle := lv.m.idle
	wait := idle - lv.m.clock().Sub(lv.lastActive)
	if lv.inflight > 0 || wait > 0 {
		if lv.inflight > 0 || wait <= 0 {
			wait = idle
		}
		lv.idleTimer = time.AfterFunc(wait, lv.idleCheck)
		lv.mu.Unlock()
		return
	}
	lv.idleTimer = nil
	conns := make([]*trackedConn, 0, len(lv.conns))
	for c := range lv.conns {
		conns = append(conns, c)
	}
	lv.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// stop ends the idle timer for good (the lease is closing).
func (lv *live) stop() {
	lv.mu.Lock()
	lv.closed = true
	if lv.idleTimer != nil {
		lv.idleTimer.Stop()
		lv.idleTimer = nil
	}
	lv.mu.Unlock()
}

func (lv *live) openConns() int {
	lv.mu.Lock()
	defer lv.mu.Unlock()
	return len(lv.conns)
}

// shouldAuditDenied reports whether a refusal is worth a new audit row; refusals are
// rate-limited per lease so a scan of the port cannot flood the audit table.
func (lv *live) shouldAuditDenied() bool {
	lv.mu.Lock()
	defer lv.mu.Unlock()
	now := lv.m.clock()
	if !lv.lastDenied.IsZero() && now.Sub(lv.lastDenied) < lv.m.authAuditEvery {
		return false
	}
	lv.lastDenied = now
	return true
}

// trackedConn counts itself against the lease's connection limit and reports traffic as
// activity. A hijacked (WebSocket) connection is still this value, so it keeps counting.
type trackedConn struct {
	net.Conn
	lv   *live
	once sync.Once
}

func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.lv.touch()
	}
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.lv.touch()
	}
	return n, err
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.lv.mu.Lock()
		delete(c.lv.conns, c)
		c.lv.mu.Unlock()
	})
	return err
}

// limitListener enforces the per-lease connection cap: a connection over the limit is
// answered with a bare 503 and never reaches the HTTP server.
type limitListener struct {
	net.Listener
	lv *live
}

const busyResponse = "HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\nContent-Length: 0\r\nRetry-After: 1\r\n\r\n"

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.lv.mu.Lock()
		if l.lv.max > 0 && len(l.lv.conns) >= l.lv.max {
			l.lv.mu.Unlock()
			go func() {
				_ = c.SetWriteDeadline(time.Now().Add(time.Second))
				_, _ = c.Write([]byte(busyResponse))
				_ = c.Close()
			}()
			continue
		}
		tc := &trackedConn{Conn: c, lv: l.lv}
		l.lv.conns[tc] = struct{}{}
		l.lv.touchLocked()
		l.lv.mu.Unlock()
		return tc, nil
	}
}
