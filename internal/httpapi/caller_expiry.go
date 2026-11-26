package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/inbox"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// Token expiry and last use of stored callers (design 2026-11-14 §4, plan R7 / R8).
//
// An expired caller stays in the snapshot (callerEntry.expiresAt) so the request can be
// refused as `token_expired` and audited; the decision is taken right after the constant-time
// scan matched, never inside it, so the scan's cost does not depend on expiry.

// authNow is the clock expiry is judged by.
func (s *Server) authNow() time.Time {
	if s.authClock != nil {
		return s.authClock()
	}
	return time.Now()
}

// callerTokenExpired reports whether the entry's token is past its expiry. A caller with no
// expiry (a file caller, a stored one left at `never`) is never expired.
func (s *Server) callerTokenExpired(ce callerEntry) bool {
	return ce.expiresAt > 0 && s.authNow().Unix() >= ce.expiresAt
}

// denyExpiredToken answers 401 for an expired token and writes the auth_audit line
// `request.denied reason=token_expired`.
func (s *Server) denyExpiredToken(c *rux.Context, ce callerEntry) {
	if st := s.authStore(); st != nil {
		_ = st.InsertAuthAudit(jobstore.AuthAuditEntry{
			Actor: ce.id, Action: "request.denied", Subject: authz.SubjectOf(authz.SubjectCaller, ce.id),
			SessionID: requestSessionID(c), Method: c.Req.Method, Path: c.Req.URL.Path,
			Status: http.StatusUnauthorized, Reason: "token_expired",
		})
	}
	writeError(c, http.StatusUnauthorized, "unauthorized", "the caller token has expired; ask an administrator to rotate it")
}

// callerUsageInterval is how often the last-used stamps are written (plan R8).
const callerUsageInterval = 60 * time.Second

// usageSink is the store seam of the usage tracker (the real one is *jobstore.Store).
type usageSink interface {
	TouchCallersUsed(used map[string]int64) error
}

// callerUsage remembers, in memory, when each stored caller last authenticated, and writes
// the stamps in one batch every callerUsageInterval and at shutdown, so no request writes
// the database. Like the audit recorder it owns no long-lived goroutine: a timer exists only
// while a stamp waits.
type callerUsage struct {
	sink     func() usageSink
	interval time.Duration
	now      func() time.Time

	mu    sync.Mutex
	used  map[string]int64
	timer *time.Timer
	// flushMu serialises writes so an older batch never lands after a newer one.
	flushMu sync.Mutex
}

func newCallerUsage(sink func() usageSink, interval time.Duration, now func() time.Time) *callerUsage {
	return &callerUsage{sink: sink, interval: interval, now: now, used: map[string]int64{}}
}

func (s *Server) newCallerUsage() *callerUsage {
	return newCallerUsage(func() usageSink {
		if st := s.authStore(); st != nil {
			return st
		}
		return nil
	}, callerUsageInterval, s.authNow)
}

// mark records that id authenticated just now. Safe on a nil tracker.
func (u *callerUsage) mark(id string) {
	if u == nil || id == "" {
		return
	}
	at := u.now().Unix()
	u.mu.Lock()
	u.used[id] = at
	if u.timer == nil {
		u.timer = time.AfterFunc(u.interval, u.flush)
	}
	u.mu.Unlock()
}

// flush writes the waiting stamps. A failed write is dropped: the next use stamps again.
func (u *callerUsage) flush() {
	if u == nil {
		return
	}
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	u.mu.Lock()
	batch := u.used
	u.used = map[string]int64{}
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	u.mu.Unlock()
	if len(batch) == 0 || u.sink == nil {
		return
	}
	if sink := u.sink(); sink != nil {
		_ = sink.TouchCallersUsed(batch)
	}
}

// noteCallerUsed stamps a successful authentication of a STORED caller (only those have a
// last_used_at column; file callers and workers are not tracked).
func (s *Server) noteCallerUsed(ce callerEntry) {
	if ce.tokenHash != "" && ce.kind == callerKindUser {
		s.usage.mark(ce.id)
	}
}

// FlushCallerUsage writes the waiting last-used stamps now (tests, shutdown).
func (s *Server) FlushCallerUsage() { s.usage.flush() }

// Drain writes everything this server still holds in memory — the admin audit queue and the
// last-used stamps — and stops recording audit lines. serve calls it once the HTTP server
// has stopped, before the database closes.
func (s *Server) Drain() {
	s.stageSvc.once.Do(func() {}) // orders the read below after a build in another goroutine
	if svc := s.stageSvc.svc; svc != nil {
		svc.Drain()
	}
	s.CloseAudit()
	s.FlushCallerUsage()
}

// callerTokenCards builds the 「token about to expire」 cards for the requester, or none when
// the requester cannot rotate a token (only a person holding auth.manage manages callers).
func (s *Server) callerTokenCards(c *rux.Context) ([]today.Card, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || callerKindFromCtx(c) != callerKindUser || !s.can(c, authz.PermAuthManage) {
		return nil, nil
	}
	return inbox.CallerTokenCards(s.jobs.Meta(), s.requestLang(c), s.authNow())
}
