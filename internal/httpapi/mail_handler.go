package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// The gofer mailbox over HTTP (design 2026-10-16「二、gofer 信箱」, plan M3). The business
// (address resolution, limits, threads, the capture box) is internal/mail; this file binds
// the requests, derives WHO is writing (the letter's origin), and applies the permission table
// and the caller ceiling to the resolved recipient — the mailbox never reads permissions.

// mailRefusal is an authorization failure found while a letter is being resolved (it is
// returned from mail.SendInput.Authorize, so nothing has been stored yet).
type mailRefusal struct {
	// status is 403 (a permission) or 404 (outside the caller's ceiling).
	status int
	perms  []string
	kind   string
	id     string
	detail string
}

func (e *mailRefusal) Error() string { return "mail refused: " + e.detail }

func mailForbidden(detail string, perms ...string) *mailRefusal {
	return &mailRefusal{status: http.StatusForbidden, perms: perms, detail: detail}
}

// mailReady answers 503 when no mailbox is wired (a server without a job store).
func (s *Server) mailReady(c *rux.Context) bool {
	if s.mail == nil || s.jobs == nil || s.jobs.Meta() == nil {
		writeError(c, http.StatusServiceUnavailable, "mailbox unavailable", "no job store wired on this server")
		return false
	}
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "mail not permitted for this caller", "a worker token is a transport credential, not a mail sender")
		return false
	}
	return true
}

// writeMailError maps the mailbox's errors onto HTTP statuses.
func (s *Server) writeMailError(c *rux.Context, err error) {
	var ref *mailRefusal
	switch {
	case errors.As(err, &ref):
		if ref.status == http.StatusNotFound {
			writeOutOfScope(c, ref.kind, ref.id)
			return
		}
		if len(ref.perms) > 0 {
			s.writePermissionDenied(c, ref.perms...)
			return
		}
		writeError(c, http.StatusForbidden, "mail not permitted", ref.detail)
	case errors.Is(err, mail.ErrRateLimited):
		writeError(c, http.StatusTooManyRequests, "rate limited", "this sender passed its mail rate limit; retry shortly")
	case errors.Is(err, mail.ErrMailTooLong):
		writeError(c, http.StatusRequestEntityTooLarge, "mail too long", err.Error())
	case errors.Is(err, mail.ErrInvalidAddress), errors.Is(err, mail.ErrUnsupportedAddress),
		errors.Is(err, mail.ErrInvalidBody), errors.Is(err, mail.ErrInvalidOrigin), errors.Is(err, jobstore.ErrMailInvalid):
		writeError(c, http.StatusBadRequest, "invalid mail", err.Error())
	case errors.Is(err, mail.ErrMailNotFound):
		writeError(c, http.StatusNotFound, "not found", "mail not found")
	case errors.Is(err, jobstore.ErrMailState):
		writeError(c, http.StatusConflict, "mail state conflict", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "mail failed", err.Error())
	}
}

// ---- views ----

type mailPartyView struct {
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Caller  string `json:"caller,omitempty"`
	Display string `json:"display,omitempty"`
}

type mailView struct {
	ID            int64         `json:"id"`
	ThreadID      string        `json:"thread_id"`
	ReplyTo       int64         `json:"reply_to,omitempty"`
	From          mailPartyView `json:"from"`
	ToAddress     string        `json:"to_address"`
	To            mailPartyView `json:"to"`
	JobID         string        `json:"job_id,omitempty"`
	Urgent        bool          `json:"urgent,omitempty"`
	Body          string        `json:"body"`
	State         string        `json:"state"`
	Attempts      int           `json:"attempts,omitempty"`
	LastError     string        `json:"last_error,omitempty"`
	CreatedAt     int64         `json:"created_at"`
	DeliveredAt   int64         `json:"delivered_at,omitempty"`
	ReadAt        int64         `json:"read_at,omitempty"`
	DeliverBy     int64         `json:"deliver_by,omitempty"`
	CaptureItemID string        `json:"capture_item_id,omitempty"`
}

func toMailView(m jobstore.Mail) mailView {
	return mailView{
		ID: m.ID, ThreadID: m.ThreadID, ReplyTo: m.ReplyTo,
		From:      mailPartyView{Kind: m.FromKind, ID: m.FromID, Caller: m.FromCaller, Display: mail.OriginOf(m).Display()},
		ToAddress: m.ToAddress,
		To:        mailPartyView{Kind: m.ToKind, ID: m.ToID, Caller: m.ToCaller},
		JobID:     m.JobID, Urgent: m.Urgent, Body: m.Body, State: m.State, Attempts: m.Attempts,
		LastError: m.LastError, CreatedAt: m.CreatedAt, DeliveredAt: m.DeliveredAt, ReadAt: m.ReadAt,
		DeliverBy: m.DeliverBy, CaptureItemID: m.CaptureItemID,
	}
}

type mailCandidateView struct {
	Kind    string   `json:"kind"`
	ID      string   `json:"id"`
	Topic   string   `json:"topic,omitempty"`
	Source  string   `json:"source,omitempty"`
	Offline bool     `json:"offline,omitempty"`
	Why     []string `json:"why,omitempty"`
	Display string   `json:"display"`
}

// mailSendView is the answer of a send / reply / readdress.
type mailSendView struct {
	Mail          mailView            `json:"mail"`
	Held          bool                `json:"held"`
	CaptureItemID string              `json:"capture_item_id,omitempty"`
	Candidates    []mailCandidateView `json:"candidates,omitempty"`
}

func toMailSendView(r mail.SendResult) mailSendView {
	out := mailSendView{Mail: toMailView(r.Mail), Held: r.Held, CaptureItemID: r.CaptureItemID}
	for _, c := range r.Candidates {
		out.Candidates = append(out.Candidates, mailCandidateView{
			Kind: c.Kind, ID: c.ID, Topic: c.Topic, Source: c.Source, Offline: c.Offline, Why: c.Why, Display: c.Display(),
		})
	}
	return out
}

// ---- origin ----

// mailOrigin decides who is writing (设计「发件出处与信任级别」): a job credential is the job (the
// steward's too); a user with the session header is that session after the server checked it
// exists, has not ended and belongs to the same caller — an attribution inside one caller, not
// an authentication; a user without one is the caller. ok=false: the answer is written.
func (s *Server) mailOrigin(c *rux.Context) (mail.Origin, bool) {
	if jc, isJob := jobCallerFromCtx(c); isJob {
		rec, found := s.jobs.Get(jc.JobID)
		if !found {
			writeError(c, http.StatusForbidden, "caller ceiling", "cannot determine the job behind this credential")
			return mail.Origin{}, false
		}
		return mail.Origin{Kind: jobstore.MailKindJob, ID: jc.JobID, Caller: rec.CallerID}, true
	}
	caller := callerFromCtx(c)
	sid := requestSessionID(c)
	if sid == "" {
		return mail.Origin{Kind: jobstore.MailKindCaller, Caller: caller}, true
	}
	anchor, found, err := s.jobs.Meta().SessionGrantAnchor(sid)
	switch {
	case err != nil:
		writeError(c, http.StatusInternalServerError, "mail origin failed", err.Error())
		return mail.Origin{}, false
	case !found:
		writeError(c, http.StatusNotFound, "unknown session", "the session named by "+sessionHeader+" is not registered")
		return mail.Origin{}, false
	case anchor.Ended():
		writeError(c, http.StatusConflict, "session ended", "the session named by "+sessionHeader+" has ended")
		return mail.Origin{}, false
	case anchor.Owner != "" && anchor.Owner != caller:
		writeError(c, http.StatusForbidden, "session belongs to another caller", "the session named by "+sessionHeader+" is not this caller's")
		return mail.Origin{}, false
	}
	// A ceiling bounds the sending session too (its project must be inside).
	if !s.requireResourceScope(c, "session", sid) {
		return mail.Origin{}, false
	}
	return mail.Origin{Kind: jobstore.MailKindSession, ID: sid, Caller: caller}, true
}

// ---- relation of a caller to a letter ----

// mailEndProject is the project of a session / job end of a letter ("" when it has none or is
// gone).
func (s *Server) mailEndProject(kind, id string) (string, bool) {
	switch kind {
	case jobstore.MailKindSession:
		a, ok, err := s.jobs.Meta().GetAgentSession(id)
		if err != nil || !ok {
			return "", false
		}
		return a.ProjectKey, true
	case jobstore.MailKindJob:
		rec, ok := s.jobs.Get(id)
		if !ok {
			return "", false
		}
		return rec.ProjectKey, true
	}
	return "", false
}

// mailRecipientOwner is the caller a letter's recipient belongs to. found=false: the session /
// job is gone, so nobody can be shown to own it. An ownerless session counts as everybody's
// (the same convention as sessionAccess).
func (s *Server) mailRecipientOwner(m jobstore.Mail) (owner string, found bool) {
	switch m.ToKind {
	case jobstore.MailKindSession:
		a, ok, err := s.jobs.Meta().GetAgentSession(m.ToID)
		if err != nil || !ok {
			return "", false
		}
		return a.CallerID, true
	case jobstore.MailKindJob:
		rec, ok := s.jobs.Get(m.ToID)
		if !ok {
			return "", false
		}
		return rec.CallerID, true
	case jobstore.MailKindCaller:
		return m.ToCaller, true
	}
	return "", false
}

// mailIsRecipientSide: the caller is the owner of the recipient (an ownerless session is
// everybody's).
func (s *Server) mailIsRecipientSide(caller string, m jobstore.Mail) bool {
	owner, found := s.mailRecipientOwner(m)
	if !found {
		return false
	}
	if owner == "" {
		return m.ToKind == jobstore.MailKindSession
	}
	return owner == caller
}

// mailJobRelated: a job credential only touches the letters of its own job.
func mailJobRelated(jobID string, m jobstore.Mail) bool {
	return (m.FromKind == jobstore.MailKindJob && m.FromID == jobID) ||
		(m.ToKind == jobstore.MailKindJob && m.ToID == jobID)
}

// mailOthersPerm is the `.others` permission a user without a relation to the letter needs:
// by the recipient's type (设计「路由与权限」), and write=false is the read form.
func mailOthersPerm(m jobstore.Mail, write bool) string {
	if m.ToKind == jobstore.MailKindJob {
		if write {
			return authz.PermJobManageOthers
		}
		return authz.PermJobReadOthers
	}
	return authz.PermSessionReplyOthers
}

// mayTouchMail decides whether the caller may read (write=false) / act on a letter and, for a
// refusal, which permission it lacked ("" = a rule without a permission).
func (s *Server) mayTouchMail(c *rux.Context, m jobstore.Mail, write bool) (bool, string) {
	if jc, isJob := jobCallerFromCtx(c); isJob {
		return mailJobRelated(jc.JobID, m), ""
	}
	if callerKindFromCtx(c) != callerKindUser {
		return false, ""
	}
	caller := callerFromCtx(c)
	if m.FromCaller == caller || s.mailIsRecipientSide(caller, m) {
		return true, ""
	}
	perm := mailOthersPerm(m, write)
	return s.can(c, perm), perm
}

// requireMailAccess loads the letter and applies mayTouchMail, answering 404 / 403 itself.
func (s *Server) requireMailAccess(c *rux.Context, write bool) (jobstore.Mail, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid mail id", c.Param("id"))
		return jobstore.Mail{}, false
	}
	m, ok, err := s.jobs.Meta().GetMail(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "mail failed", err.Error())
		return jobstore.Mail{}, false
	}
	if !ok {
		writeError(c, http.StatusNotFound, "not found", "mail not found")
		return jobstore.Mail{}, false
	}
	if allowed, perm := s.mayTouchMail(c, m, write); !allowed {
		if perm != "" {
			s.writePermissionDenied(c, perm)
		} else {
			writeError(c, http.StatusForbidden, "mail not permitted", "this credential may only touch the letters of its own job")
		}
		return jobstore.Mail{}, false
	}
	return m, true
}

// ---- recipient authorization ----

// mailAuthorize builds the judgement of a RESOLVED recipient: the caller ceiling first (outside
// is a 404, like a missing resource), then the permission table. replying skips the sender-side
// rules (a reply goes back to whoever wrote; the right to answer is the right to touch the
// letter, decided by mayTouchMail) but never the ceiling.
func (s *Server) mailAuthorize(c *rux.Context, org mail.Origin, replying bool) func(mail.Target) error {
	ceil := s.ceilingOf(c)
	jc, isJob := jobCallerFromCtx(c)
	var allowedSessions map[string]bool
	if isJob && !replying {
		allowedSessions = map[string]bool{}
		if rec, ok := s.jobs.Get(jc.JobID); ok {
			if rec.SourceSessionID != "" {
				allowedSessions[rec.SourceSessionID] = true
			}
			if rec.PlanID != "" {
				if p, ok, err := s.jobs.Meta().GetPlan(rec.PlanID); err == nil && ok && p.SupervisorSessionID != "" {
					allowedSessions[p.SupervisorSessionID] = true
				}
			}
		}
	}
	return func(t mail.Target) error {
		switch t.Kind {
		case jobstore.MailKindSession, jobstore.MailKindJob:
			project, found := s.mailEndProject(t.Kind, t.ID)
			if !found {
				return nil // resolution found it a moment ago; the store judges absence
			}
			if !ceilingAllowsProject(ceil, project) {
				return &mailRefusal{status: http.StatusNotFound, kind: t.Kind, id: t.ID}
			}
		}
		if replying {
			return nil
		}
		if isJob {
			switch t.Kind {
			case jobstore.MailKindCaller:
				return nil
			case jobstore.MailKindSession:
				if allowedSessions[t.ID] {
					return nil
				}
			}
			return mailForbidden("a job credential may only mail its job's source session, its plan's supervisor session and its caller")
		}
		switch t.Kind {
		case jobstore.MailKindSession:
			a, ok, err := s.jobs.Meta().GetAgentSession(t.ID)
			if err != nil || !ok {
				return nil
			}
			if !s.mayReplyToSession(c, a.CallerID) {
				return mailForbidden("another caller's session", authz.PermSessionReplyOthers)
			}
		case jobstore.MailKindJob:
			rec, ok := s.jobs.Get(t.ID)
			if !ok {
				return nil
			}
			if !s.mayManageJob(c, rec, false) {
				return mailForbidden("another caller's job", authz.PermJobManageOthers)
			}
		}
		return nil
	}
}

// mailVisible is the reader-permission predicate a topic: / path: lookup is filtered with
// (设计「候选按读者的 session.read.others 过滤」), narrowed by the caller's ceiling.
func (s *Server) mailVisible(c *rux.Context) func(jobstore.AgentSession) bool {
	inScope := s.scopeFilter(c)
	caller := callerFromCtx(c)
	return func(a jobstore.AgentSession) bool {
		if !inScope(a.ProjectKey) {
			return false
		}
		if a.CallerID == "" || a.CallerID == caller {
			return true
		}
		return s.can(c, authz.PermSessionReadOthers)
	}
}

// mailVisibleFor is mailVisible for a search address only (topic: / path:). An explicit
// session: / job: address is judged by Authorize — the permission table's 403 / the ceiling's
// 404 — and must not be silently hidden here.
func (s *Server) mailVisibleFor(c *rux.Context, to string) func(jobstore.AgentSession) bool {
	if addr, err := mail.ParseAddress(to); err == nil && (addr.Scheme == mail.SchemeTopic || addr.Scheme == mail.SchemePath) {
		return s.mailVisible(c)
	}
	return nil
}

// ---- handlers ----

type mailSendReq struct {
	To      string `json:"to"`
	Body    string `json:"body"`
	Urgent  bool   `json:"urgent,omitempty"`
	Project string `json:"project,omitempty"`
}

// checkJobMailAddress: a job credential names its recipient directly — a session or its
// caller's box; a search address would let it probe sessions it may not know about.
func checkJobMailAddress(to string) error {
	addr, err := mail.ParseAddress(to)
	if err != nil {
		return err
	}
	switch addr.Scheme {
	case mail.SchemeSession, mail.SchemeCaller:
		return nil
	}
	return mailForbidden("a job credential may only mail a session or its caller")
}

// POST /v1/mail {to, body, urgent?, project?}
func (s *Server) handleMailSend(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	var req mailSendReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	org, ok := s.mailOrigin(c)
	if !ok {
		return
	}
	if _, isJob := jobCallerFromCtx(c); isJob {
		if err := checkJobMailAddress(req.To); err != nil {
			s.writeMailError(c, err)
			return
		}
	}
	// BodyProject: a named project bounds the lookup; the recipient's own project is judged in
	// Authorize once the address is resolved.
	if p := strings.TrimSpace(req.Project); p != "" && !s.requireProjectScope(c, p) {
		return
	}
	res, err := s.mail.Send(mail.SendInput{
		To: req.To, Body: req.Body, Urgent: req.Urgent, Origin: org, Project: strings.TrimSpace(req.Project),
		Visible: s.mailVisibleFor(c, req.To), Roots: s.projectRoots, Authorize: s.mailAuthorize(c, org, false),
	})
	c.Set(ctxCeilingChecked, true)
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	c.JSON(http.StatusOK, toMailSendView(res))
}

// GET /v1/mail/{id}
func (s *Server) handleMailGet(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	m, ok := s.requireMailAccess(c, false)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, toMailView(m))
}

type mailReplyReq struct {
	Body   string `json:"body"`
	Urgent bool   `json:"urgent,omitempty"`
}

// POST /v1/mail/{id}/reply {body, urgent?}
func (s *Server) handleMailReply(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	var req mailReplyReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	m, ok := s.requireMailAccess(c, true)
	if !ok {
		return
	}
	org, ok := s.mailOrigin(c)
	if !ok {
		return
	}
	res, err := s.mail.Reply(mail.ReplyInput{
		ID: m.ID, Body: req.Body, Urgent: req.Urgent, Origin: org, Authorize: s.mailAuthorize(c, org, true),
	})
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	c.JSON(http.StatusOK, toMailSendView(res))
}

// POST /v1/mail/{id}/ack — the recipient read it: reading IS delivery (state delivered, read_at
// stamped). Somebody who is only the SENDER (or holds an `.others` permission) gets the letter
// back unchanged: they did not read it as its recipient.
func (s *Server) handleMailAck(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	m, ok := s.requireMailAccess(c, true)
	if !ok {
		return
	}
	recipient := false
	if jc, isJob := jobCallerFromCtx(c); isJob {
		recipient = m.ToKind == jobstore.MailKindJob && m.ToID == jc.JobID
	} else {
		recipient = s.mailIsRecipientSide(callerFromCtx(c), m)
	}
	if recipient {
		got, changed, err := s.jobs.Meta().MarkMailRead(m.ID)
		if err != nil {
			s.writeMailError(c, err)
			return
		}
		if changed {
			m = got
		}
	}
	c.JSON(http.StatusOK, toMailView(m))
}

// POST /v1/mail/{id}/cancel — the sender (or the recipient's side) withdraws a letter that has
// not been delivered; 409 when it already was.
func (s *Server) handleMailCancel(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	m, ok := s.requireMailAccess(c, true)
	if !ok {
		return
	}
	got, changed, err := s.jobs.Meta().CancelMail(m.ID, "cancelled by "+callerFromCtx(c))
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	if !changed {
		writeError(c, http.StatusConflict, "mail state conflict", "letter "+strconv.FormatInt(m.ID, 10)+" is "+m.State+" and cannot be cancelled")
		return
	}
	c.JSON(http.StatusOK, toMailView(got))
}

type mailReaddressReq struct {
	To      string `json:"to"`
	Project string `json:"project,omitempty"`
}

// POST /v1/mail/{id}/readdress {to} — hand a held letter to a new address (a fresh send,
// judged against the NEW recipient).
func (s *Server) handleMailReaddress(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	if _, isJob := jobCallerFromCtx(c); isJob {
		writeError(c, http.StatusForbidden, "mail not permitted", "a job credential may not readdress a letter")
		return
	}
	var req mailReaddressReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	m, ok := s.requireMailAccess(c, true)
	if !ok {
		return
	}
	org, ok := s.mailOrigin(c)
	if !ok {
		return
	}
	if p := strings.TrimSpace(req.Project); p != "" && !s.requireProjectScope(c, p) {
		return
	}
	res, err := s.mail.Readdress(mail.ReaddressInput{
		ID: m.ID, To: req.To, Origin: org, Project: strings.TrimSpace(req.Project),
		Visible: s.mailVisibleFor(c, req.To), Roots: s.projectRoots, Authorize: s.mailAuthorize(c, org, false),
	})
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	c.JSON(http.StatusOK, toMailSendView(res))
}

const (
	mailListDefault = 50
	mailListMax     = 200
	// mailListScanPages bounds how many store pages a filtered listing walks.
	mailListScanPages = 10
)

// GET /v1/mail?box=in|out|all&thread=&state=&session=&limit=&offset=
//
// The listing is the FILTER form of the permission table: the caller's own letters (sent or
// received) need nothing; another caller's letters appear only with the `.others` permission of
// their recipient's type; a job credential sees its own job's letters; a ceiling drops letters
// whose both ends are outside it.
func (s *Server) handleMailList(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	box := strings.TrimSpace(c.Query("box"))
	if box == "" {
		box = "all"
	}
	if box != "in" && box != "out" && box != "all" {
		writeError(c, http.StatusBadRequest, "invalid box", "box must be in, out or all")
		return
	}
	limit := mailListDefault
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > mailListMax {
		limit = mailListMax
	}
	offset := 0
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		offset = n
	}
	var states []string
	for _, st := range strings.Split(c.Query("state"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			states = append(states, st)
		}
	}
	thread := strings.TrimSpace(c.Query("thread"))
	session := strings.TrimSpace(c.Query("session"))
	opts := jobstore.ListMailOpts{ThreadID: thread, States: states, Limit: jobstore.MailListMaxLimit, OldestFirst: thread != ""}
	if session != "" {
		// A session filter narrows the store query when the box fixes the side.
		switch box {
		case "in":
			opts.ToKind, opts.ToID = jobstore.MailKindSession, session
		case "out":
			opts.FromKind, opts.FromID = jobstore.MailKindSession, session
		}
	}

	ceil := s.ceilingOf(c)
	caller := callerFromCtx(c)
	jc, isJob := jobCallerFromCtx(c)
	owners := map[string]struct {
		owner string
		found bool
	}{}
	recipientOwner := func(m jobstore.Mail) (string, bool) {
		key := m.ToKind + "/" + m.ToID + "/" + m.ToCaller
		if v, ok := owners[key]; ok {
			return v.owner, v.found
		}
		o, f := s.mailRecipientOwner(m)
		owners[key] = struct {
			owner string
			found bool
		}{o, f}
		return o, f
	}
	keep := func(m jobstore.Mail) bool {
		if session != "" && !((m.ToKind == jobstore.MailKindSession && m.ToID == session && box != "out") ||
			(m.FromKind == jobstore.MailKindSession && m.FromID == session && box != "in")) {
			return false
		}
		if ceil != nil && !s.mailInCeiling(ceil, m) {
			return false
		}
		if isJob {
			switch box {
			case "in":
				return m.ToKind == jobstore.MailKindJob && m.ToID == jc.JobID
			case "out":
				return m.FromKind == jobstore.MailKindJob && m.FromID == jc.JobID
			}
			return mailJobRelated(jc.JobID, m)
		}
		if callerKindFromCtx(c) != callerKindUser {
			return false
		}
		in := false
		if owner, found := recipientOwner(m); found && (owner == caller || (owner == "" && m.ToKind == jobstore.MailKindSession)) {
			in = true
		}
		out := m.FromCaller == caller
		switch box {
		case "in":
			return in
		case "out":
			return out
		}
		return in || out || s.can(c, mailOthersPerm(m, false))
	}

	result := make([]mailView, 0, limit)
	skipped := 0
	for page := 0; page < mailListScanPages && len(result) < limit; page++ {
		opts.Offset = page * jobstore.MailListMaxLimit
		rows, err := s.jobs.Meta().ListMail(opts)
		if err != nil {
			s.writeMailError(c, err)
			return
		}
		for _, m := range rows {
			if !keep(m) {
				continue
			}
			if skipped < offset {
				skipped++
				continue
			}
			result = append(result, toMailView(m))
			if len(result) >= limit {
				break
			}
		}
		if len(rows) < jobstore.MailListMaxLimit {
			break
		}
	}
	c.JSON(http.StatusOK, map[string]any{"mail": result, "count": len(result)})
}

// mailInCeiling reports whether a letter has an end inside the ceiling: the recipient's or the
// sender's session / job project. A letter with neither (caller to caller) is outside a
// project-restricting ceiling.
func (s *Server) mailInCeiling(ceil *config.CallerCeiling, m jobstore.Mail) bool {
	if len(ceil.Projects) == 0 {
		return true
	}
	for _, end := range []struct{ kind, id string }{{m.ToKind, m.ToID}, {m.FromKind, m.FromID}} {
		if end.id == "" {
			continue
		}
		if project, found := s.mailEndProject(end.kind, end.id); found && ceilingAllowsProject(ceil, project) {
			return true
		}
	}
	return false
}

// mailScope is the resolver of Resource("mail"): the letter is inside when its recipient's or
// its sender's project is.
func mailScope(s *Server, ceil *config.CallerCeiling, id string) (bool, bool, error) {
	n, ok := parseInt64(id)
	if !ok || s.jobs == nil || s.jobs.Meta() == nil {
		return false, false, nil
	}
	m, found, err := s.jobs.Meta().GetMail(n)
	if err != nil || !found {
		return false, false, err
	}
	return s.mailInCeiling(ceil, m), true, nil
}
