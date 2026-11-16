package httpapi

import (
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

// Admin audit (design 2026-11-14 §1, plan R1–R4): every WRITE route is recorded in
// admin_audit after it ran — including the requests refused by authentication, the
// credential whitelists, authzGate or the rate limit — with metadata only.
// The request body, the response body and the query VALUES never reach the table (they can
// carry secrets); the query contributes its key names.

// auditOpt is the per-route audit opt-out (routeDecl.audit).
type auditOpt uint8

const (
	// auditOn is the zero value: a write route is recorded.
	auditOn auditOpt = iota
	// auditOff: the route is exempt (NoAudit).
	auditOff
)

// NoAudit exempts a write route from admin_audit: a high-frequency route with no
// administrative meaning (a worker's own handshake / upload, a heartbeat, a log append).
// Every use must be on the reviewed list of authz_audit_test.go.
func NoAudit() auditOpt { return auditOff }

// auditable reports whether the route is recorded: a write route not marked NoAudit.
func (d routeDecl) auditable() bool {
	return d.audit == auditOn && d.effectiveRW(d.key) == rwWrite
}

// ctxAuditError carries the refusal reason gofer itself determined for the request
// (401 / 403 / 429); see noteAuditError.
const ctxAuditError = "audit_error"

// auditErrorMax bounds the error summary.
const auditErrorMax = 200

// noteAuditError remembers the short reason of a refusal gofer itself produced, for the
// audit line. Only the fixed `msg` phrase is kept — never the detail and never the body —
// so nothing a caller or a handler put into the response can reach the table.
func noteAuditError(c *rux.Context, status int, msg string) {
	switch status {
	case 401, 403, 429:
		if len(msg) > auditErrorMax {
			msg = msg[:auditErrorMax]
		}
		c.Set(ctxAuditError, msg)
	}
}

// auditMiddleware records one write request after the whole chain ran, so the final status —
// a 401 from authentication, a 403 from a credential whitelist or the route gate, a 429 from
// the rate limit — is known. Whether the request is auditable is decided by the declaration
// of the MATCHED route (`<METHOD> <route template>`, the keying wrapRoutes uses): only a
// write route not marked NoAudit is recorded, and a request that matched no route (404) is
// not. It never changes the response and never fails the request.
func (s *Server) auditMiddleware(c *rux.Context) {
	start := time.Now()
	c.Next()
	rt := c.Route()
	if rt == nil {
		return
	}
	key := canonicalRouteKey(c.Req.Method, rt.Path())
	decl, ok := routeDecls[key]
	if !ok {
		return
	}
	decl.key = key
	if !decl.auditable() {
		return
	}
	s.audit.add(buildAuditEntry(c, decl, time.Since(start)))
}

// buildAuditEntry derives the admin_audit row from the finished request.
func buildAuditEntry(c *rux.Context, decl routeDecl, took time.Duration) jobstore.AdminAuditEntry {
	method, _, _ := strings.Cut(decl.key, " ")
	e := jobstore.AdminAuditEntry{
		CallerID:   callerFromCtx(c),
		CallerKind: callerKindFromCtx(c),
		SessionID:  requestSessionID(c),
		Method:     method,
		Route:      decl.key,
		Target:     auditTarget(c, decl.key),
		QueryKeys:  auditQueryKeys(c),
		Status:     c.StatusCode(),
		DurationMs: took.Milliseconds(),
		IP:         clientIP(c),
	}
	if jc, ok := jobCallerFromCtx(c); ok {
		e.JobID = jc.JobID
	}
	if v, ok := c.Get(ctxAuditError); ok {
		e.Error, _ = v.(string)
	}
	return e
}

// auditTarget joins the route's path parameters as `name=value`, in path order.
func auditTarget(c *rux.Context, key string) string {
	_, path, _ := strings.Cut(key, " ")
	var parts []string
	for _, seg := range strings.Split(path, "/") {
		if len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}' {
			name := seg[1 : len(seg)-1]
			parts = append(parts, name+"="+c.Param(name))
		}
	}
	return strings.Join(parts, " ")
}

// auditQueryKeys lists the query parameter NAMES (sorted, comma-joined); values are dropped.
func auditQueryKeys(c *rux.Context) string {
	q := c.Req.URL.Query()
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// Recorder bounds (plan R2).
const (
	auditQueueMax   = 1024
	auditBatchMax   = 100
	auditFlushEvery = time.Second
)

// auditSink is the store seam of the recorder (the real one is *jobstore.Store).
type auditSink interface {
	InsertAdminAuditBatch(entries []jobstore.AdminAuditEntry) error
}

// auditRecorder is the bounded, asynchronous writer of admin_audit. Entries wait in a queue
// of auditQueueMax; a flush runs when auditBatchMax have gathered or auditFlushEvery after
// the first waiting one, and never more than one flush runs at a time. A full queue drops
// the NEW entry and counts it (gofer_audit_dropped_total), so a slow database can never
// slow a request. It owns no long-lived goroutine: a timer or a short flush goroutine
// exists only while entries wait, and close drains what is left (G051).
type auditRecorder struct {
	sink    func() auditSink // resolved at flush time (the store may be wired after New)
	dropped func()           // counts one dropped entry (metrics; may be nil)

	mu      sync.Mutex
	queue   []jobstore.AdminAuditEntry
	timer   *time.Timer
	closed  bool
	flushMu sync.Mutex // serialises flushes so batches keep their order
}

func newAuditRecorder(sink func() auditSink, dropped func()) *auditRecorder {
	return &auditRecorder{sink: sink, dropped: dropped}
}

// add queues one entry. Safe on a nil recorder.
func (r *auditRecorder) add(e jobstore.AdminAuditEntry) {
	if r == nil {
		return
	}
	if e.At == 0 {
		e.At = time.Now().Unix()
	}
	r.mu.Lock()
	if r.closed || len(r.queue) >= auditQueueMax {
		r.mu.Unlock()
		if r.dropped != nil {
			r.dropped()
		}
		return
	}
	r.queue = append(r.queue, e)
	switch {
	case len(r.queue) >= auditBatchMax:
		if r.timer != nil {
			r.timer.Stop()
			r.timer = nil
		}
		r.mu.Unlock()
		go r.flush()
	case r.timer == nil:
		r.timer = time.AfterFunc(auditFlushEvery, r.flush)
		r.mu.Unlock()
	default:
		r.mu.Unlock()
	}
}

// flush writes everything queued, batch by batch. A failed batch is logged and discarded
// (audit is best-effort; retrying forever would grow the queue behind a broken database).
func (r *auditRecorder) flush() {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	for {
		r.mu.Lock()
		if r.timer != nil {
			r.timer.Stop()
			r.timer = nil
		}
		n := len(r.queue)
		if n == 0 {
			r.mu.Unlock()
			return
		}
		if n > auditBatchMax {
			n = auditBatchMax
		}
		batch := make([]jobstore.AdminAuditEntry, n)
		copy(batch, r.queue[:n])
		r.queue = append(r.queue[:0], r.queue[n:]...)
		r.mu.Unlock()
		var sink auditSink
		if r.sink != nil {
			sink = r.sink()
		}
		if sink == nil {
			continue // no store: nothing can be recorded, drop the batch
		}
		if err := sink.InsertAdminAuditBatch(batch); err != nil {
			slog.Warn("server.admin_audit_write_failed", "component", "server", "entries", len(batch), "error", err.Error())
		}
	}
}

// close stops accepting entries and writes what is queued. Idempotent; nil-safe.
func (r *auditRecorder) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.flush()
}

// newAdminAuditRecorder wires the recorder to the metadata store and the drop counter.
func (s *Server) newAdminAuditRecorder() *auditRecorder {
	return newAuditRecorder(func() auditSink {
		if st := s.authStore(); st != nil {
			return st
		}
		return nil
	}, func() { s.metrics.AuditDropped() })
}

// FlushAudit writes the queued admin_audit lines now (tests, and callers that read the
// table right after a write).
func (s *Server) FlushAudit() { s.audit.flush() }

// CloseAudit drains the admin_audit queue and stops recording; serve calls it when the
// HTTP server has stopped, before the database closes.
func (s *Server) CloseAudit() { s.audit.close() }
