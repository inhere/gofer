package httpapi

import (
	"sync"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
)

// Caller-ceiling refusals are recorded in auth_audit (gofer-7798 phase two E7). A ceiling's 403 /
// 404 answers the caller but leaves nothing behind, and "why did that agent's job never appear" is
// asked long after the agent has forgotten its perspective. The rows are ordinary auth_audit
// entries (action `request.ceiling_denied`), so they show up next to permission denials in
// GET /v1/auth/audit and in the console's 设置 → 权限 list.
//
// Throttling: one row per (caller, category, resource kind) per minute, kept IN PROCESS. A
// multi-process deployment therefore writes up to one row per process per minute (and loses the
// window on a restart) — this is an audit trail, not a counter: the point is that a looping agent
// cannot fill the table with its own refusals, not that the attempts are counted exactly.
const ceilingAuditWindow = time.Minute

// ceilingDenial is one refusal to record.
type ceilingDenial struct {
	// route is the declared route key ("POST /v1/jobs/{id}/comments") or the operation name.
	route string
	// status is the status the caller was answered with.
	status int
	// category buckets the refusal for the throttle and is the readable reason on its own; it must
	// not carry the resource id (the key would then never throttle).
	category string
	// text is the readable reason when it is more specific than category (an error message); empty
	// means "use category".
	text string
	// kind is the resource type the refusal was about (project, schedule, job, …); "" when the
	// refusal is about the route / caller as a whole.
	kind string
	// id is the resource id the refusal named, when one was addressable.
	id string
}

// The categories. Keep them short and stable: they are the middle component of the throttle key.
const (
	ceilingDenyReadOnly    = "caller is read-only"
	ceilingDenyOutOfScope  = "resource outside the caller's ceiling"
	ceilingDenyGlobalState = "caller with a scope ceiling may not use a server-global route"
	ceilingDenyWorkerAdmin = "caller with a scope ceiling may not manage workers"
	ceilingDenySubmit      = "submitted job refused by the caller's ceiling"
)

type ceilingAuditKey struct {
	caller   string
	category string
	kind     string
}

var (
	ceilingAuditMu sync.Mutex
	ceilingAuditAt = map[ceilingAuditKey]time.Time{}
)

// recordCeilingDenied appends one auth_audit row for a ceiling refusal, unless the same
// (caller, category, kind) was recorded within ceilingAuditWindow. It never fails a request: a
// store error or an unwired store only loses the row.
func (s *Server) recordCeilingDenied(c *rux.Context, d ceilingDenial) {
	st := s.authStore()
	if st == nil {
		return
	}
	caller := callerFromCtx(c)
	key := ceilingAuditKey{caller: caller, category: d.category, kind: d.kind}
	now := time.Now()
	ceilingAuditMu.Lock()
	if last, ok := ceilingAuditAt[key]; ok && now.Sub(last) < ceilingAuditWindow {
		ceilingAuditMu.Unlock()
		return
	}
	ceilingAuditAt[key] = now
	ceilingAuditMu.Unlock()

	reason := d.text
	if reason == "" {
		reason = d.category
	}
	if d.kind != "" {
		reason += " (resource " + d.kind
		if d.id != "" {
			reason += " " + d.id
		}
		reason += ")"
	}
	if d.route != "" {
		reason += " [" + d.route + "]"
	}
	_ = st.InsertAuthAudit(jobstore.AuthAuditEntry{
		Actor:   caller,
		Action:  "request.ceiling_denied",
		Subject: authz.SubjectOf(authz.SubjectCaller, caller),
		Method:  c.Req.Method,
		Path:    c.Req.URL.Path,
		Status:  d.status,
		Reason:  reason,
	})
}

// routeCeilingDenial records a route-level refusal (the whole request is outside the ceiling, so
// there is no single resource to name).
func (s *Server) routeCeilingDenial(c *rux.Context, route string, status int, category string) {
	s.recordCeilingDenied(c, ceilingDenial{route: route, status: status, category: category})
}
