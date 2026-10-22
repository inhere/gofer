package httpapi

import (
	"bytes"
	"encoding/json"
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
			s.writeOutOfScope(c, ref.kind, ref.id)
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
		errors.Is(err, mail.ErrInvalidBody), errors.Is(err, mail.ErrInvalidOrigin), errors.Is(err, jobstore.ErrMailInvalid),
		errors.Is(err, mail.ErrNoRecipients), errors.Is(err, mail.ErrBroadcastAmbiguous),
		errors.Is(err, mail.ErrBroadcastTooMany), errors.Is(err, mail.ErrUrgentBroadcast):
		writeError(c, http.StatusBadRequest, "invalid mail", err.Error())
	case errors.Is(err, mail.ErrMailNotFound):
		writeError(c, http.StatusNotFound, "not found", "mail not found")
	case errors.Is(err, mail.ErrSessionNotFound):
		writeError(c, http.StatusNotFound, "session not found", err.Error())
	case errors.Is(err, mail.ErrSessionAmbiguous):
		writeError(c, http.StatusConflict, "session id ambiguous", err.Error())
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
	// Persona is who this end is (name, agent, project, state …), additive (plan M1). It is
	// omitted when the session / job it names is gone, so the console falls back to the id.
	Persona *mailPersonaView `json:"persona,omitempty"`
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
	// BroadcastID marks a copy of a broadcast (0 = a single letter); the group's addressee table
	// is in Broadcast (GET /v1/mail/{id} only).
	BroadcastID int64              `json:"broadcast_id,omitempty"`
	Broadcast   *mailBroadcastView `json:"broadcast,omitempty"`
}

func toMailView(m jobstore.Mail) mailView {
	return mailView{
		ID: m.ID, ThreadID: m.ThreadID, ReplyTo: m.ReplyTo,
		From:      mailPartyView{Kind: m.FromKind, ID: m.FromID, Caller: m.FromCaller, Display: mail.OriginOf(m).Display()},
		ToAddress: m.ToAddress,
		To:        mailPartyView{Kind: m.ToKind, ID: m.ToID, Caller: m.ToCaller},
		JobID:     m.JobID, Urgent: m.Urgent, Body: m.Body, State: m.State, Attempts: m.Attempts,
		LastError: m.LastError, CreatedAt: m.CreatedAt, DeliveredAt: m.DeliveredAt, ReadAt: m.ReadAt,
		DeliverBy: m.DeliverBy, CaptureItemID: m.CaptureItemID, BroadcastID: m.BroadcastID,
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
	// BroadcastID / Recipients / matched / delivered / skipped describe a broadcast (设计
	// 2026-10-22「D5」): the group the copies belong to and the addressee table with why a
	// selector match was skipped.
	BroadcastID int64                        `json:"broadcast_id,omitempty"`
	ThreadID    string                       `json:"thread_id,omitempty"`
	Matched     int                          `json:"matched,omitempty"`
	Delivered   int                          `json:"delivered_count,omitempty"`
	Skipped     int                          `json:"skipped_count,omitempty"`
	Recipients  []mailBroadcastRecipientView `json:"recipients,omitempty"`
}

type mailBroadcastRecipientView struct {
	Address string `json:"address"`
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	MailID  int64  `json:"mail_id,omitempty"`
	State   string `json:"state,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// mailBroadcastView is the `broadcast` section of GET /v1/mail/{id}: one broadcast's addressee
// table with each copy's own delivery state (and whether it has been answered).
type mailBroadcastView struct {
	ID         int64                        `json:"id"`
	Count      int                          `json:"count"`
	Delivered  int                          `json:"delivered"`
	Answered   int                          `json:"answered"`
	Expired    int                          `json:"expired"`
	Pending    int                          `json:"pending"`
	Recipients []mailBroadcastRecipientView `json:"recipients"`
}

func (s *Server) toMailSendView(r mail.SendResult) mailSendView {
	v := toMailView(r.Mail)
	s.newMailPersonas([]jobstore.Mail{r.Mail}).forView(&v)
	out := mailSendView{Mail: v, Held: r.Held, CaptureItemID: r.CaptureItemID}
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
			case jobstore.MailKindAgent:
				// A job credential's own submit already proved it may start work in its
				// project; a resident session is the same kind of work.
				return nil
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
		switch t.Kind {
		case jobstore.MailKindAgent:
			// A letter to an agent may START a resident session job: that costs job.submit
			// (设计 2026-10-22「D5」).
			if !s.can(c, authz.PermJobSubmit) {
				return mailForbidden("sending to an agent may start a resident session", authz.PermJobSubmit)
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
	To      mailToField `json:"to"`
	Body    string      `json:"body"`
	Urgent  bool        `json:"urgent,omitempty"`
	Project string      `json:"project,omitempty"`
}

// mailToField accepts the letter's recipient as ONE string or as an ARRAY of them (设计
// 2026-10-22「D5」): one address keeps the single-letter behaviour, several (or one `each:`
// selector) are a broadcast.
type mailToField []string

func (f *mailToField) UnmarshalJSON(b []byte) error {
	t := bytes.TrimSpace(b)
	if len(t) == 0 || string(t) == "null" {
		*f = nil
		return nil
	}
	if t[0] == '[' {
		var list []string
		if err := json.Unmarshal(t, &list); err != nil {
			return err
		}
		*f = list
		return nil
	}
	var one string
	if err := json.Unmarshal(t, &one); err != nil {
		return err
	}
	*f = []string{one}
	return nil
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

// POST /v1/mail {to: string|[string], body, urgent?, project?}
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
	to := cleanMailAddresses(req.To)
	broadcast := isBroadcastAddresses(to)
	if _, isJob := jobCallerFromCtx(c); isJob {
		if broadcast {
			writeError(c, http.StatusForbidden, "mail not permitted", "a job credential may not send a broadcast")
			return
		}
		if err := checkJobMailAddress(to[0]); err != nil {
			s.writeMailError(c, err)
			return
		}
	}
	// BodyProject: a named project bounds the lookup; the recipient's own project is judged in
	// Authorize once the address is resolved.
	if p := strings.TrimSpace(req.Project); p != "" && !s.requireProjectScope(c, p) {
		return
	}
	if broadcast {
		res, err := s.mail.Broadcast(mail.BroadcastInput{
			To: to, Body: req.Body, Urgent: req.Urgent, Origin: org, Project: strings.TrimSpace(req.Project),
			Visible: s.mailVisibleForAll(c, to), Roots: s.projectRoots, Authorize: s.mailAuthorize(c, org, false),
		})
		c.Set(ctxCeilingChecked, true)
		if err != nil {
			s.writeMailError(c, err)
			return
		}
		c.JSON(http.StatusOK, s.toMailBroadcastView(res))
		return
	}
	res, err := s.mail.Send(mail.SendInput{
		To: to[0], Body: req.Body, Urgent: req.Urgent, Origin: org, Project: strings.TrimSpace(req.Project),
		Visible: s.mailVisibleFor(c, to[0]), Roots: s.projectRoots, Authorize: s.mailAuthorize(c, org, false),
	})
	c.Set(ctxCeilingChecked, true)
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	c.JSON(http.StatusOK, s.toMailSendView(res))
}

// cleanMailAddresses trims the given addresses and drops the empty ones; nothing left means the
// caller's own capture box (one empty address, exactly as before the array form).
func cleanMailAddresses(in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// isBroadcastAddresses: several addresses, or one `each:` selector, ask for a broadcast; a single
// address keeps the single-letter meaning (several matches still park it).
func isBroadcastAddresses(to []string) bool {
	if len(to) > 1 {
		return true
	}
	return len(to) == 1 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(to[0])), mail.EachPrefix)
}

// mailVisibleForAll is mailVisibleFor for a list of addresses: the reader filter applies when any
// of them is a search address.
func (s *Server) mailVisibleForAll(c *rux.Context, to []string) func(jobstore.AgentSession) bool {
	for _, a := range to {
		if v := s.mailVisibleFor(c, a); v != nil {
			return v
		}
	}
	return nil
}

// toMailBroadcastView renders a broadcast's answer: the first copy as the letter, plus the group
// id and the addressee table.
func (s *Server) toMailBroadcastView(r mail.BroadcastResult) mailSendView {
	out := mailSendView{
		BroadcastID: r.BroadcastID, ThreadID: r.ThreadID, Matched: r.Matched,
		Delivered: len(r.Mails), Skipped: r.Skipped,
	}
	if len(r.Mails) > 0 {
		v := toMailView(r.Mails[0])
		s.newMailPersonas(r.Mails).forView(&v)
		out.Mail = v
		out.Held = r.Mails[0].State == jobstore.MailHeld
		out.CaptureItemID = r.Mails[0].CaptureItemID
	}
	for _, rc := range r.Recipients {
		out.Recipients = append(out.Recipients, mailBroadcastRecipientView{
			Address: rc.Address, Kind: rc.Kind, ID: rc.ID, MailID: rc.MailID, State: rc.State,
			Skipped: rc.Skipped, Reason: rc.Reason,
		})
	}
	return out
}

// mailBroadcastSummary reads one broadcast's copies (the addressee table of GET /v1/mail/{id}).
func (s *Server) mailBroadcastSummary(m jobstore.Mail) (*mailBroadcastView, error) {
	if m.BroadcastID <= 0 {
		return nil, nil
	}
	rows, err := s.jobs.Meta().ListMail(jobstore.ListMailOpts{BroadcastID: m.BroadcastID, Limit: jobstore.MailListMaxLimit, OldestFirst: true})
	if err != nil {
		return nil, err
	}
	v := &mailBroadcastView{ID: m.BroadcastID, Count: len(rows), Recipients: make([]mailBroadcastRecipientView, 0, len(rows))}
	for _, row := range rows {
		switch row.State {
		case jobstore.MailAnswered:
			v.Answered++
			v.Delivered++
		case jobstore.MailDelivered:
			v.Delivered++
		case jobstore.MailExpired:
			v.Expired++
		case jobstore.MailPending, jobstore.MailDelivering:
			v.Pending++
		}
		v.Recipients = append(v.Recipients, mailBroadcastRecipientView{
			Address: row.ToAddress, Kind: row.ToKind, ID: row.ToID, MailID: row.ID, State: row.State,
		})
	}
	return v, nil
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
	v := toMailView(m)
	s.newMailPersonas([]jobstore.Mail{m}).forView(&v)
	if sum, err := s.mailBroadcastSummary(m); err != nil {
		s.writeMailError(c, err)
		return
	} else {
		v.Broadcast = sum
	}
	c.JSON(http.StatusOK, v)
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
	c.JSON(http.StatusOK, s.toMailSendView(res))
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
	v := toMailView(m)
	s.newMailPersonas([]jobstore.Mail{m}).forView(&v)
	c.JSON(http.StatusOK, v)
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
	got, changed, err := s.mail.CancelMail(m.ID, "cancelled by "+callerFromCtx(c))
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	if !changed {
		writeError(c, http.StatusConflict, "mail state conflict", "letter "+strconv.FormatInt(m.ID, 10)+" is "+m.State+" and cannot be cancelled")
		return
	}
	v := toMailView(got)
	s.newMailPersonas([]jobstore.Mail{got}).forView(&v)
	c.JSON(http.StatusOK, v)
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
	c.JSON(http.StatusOK, s.toMailSendView(res))
}

const (
	mailListDefault = 50
	mailListMax     = 200
	// mailListScanPages bounds how many store pages a filtered listing walks.
	mailListScanPages = 10
)

// GET /v1/mail?box=in|out|all&thread=&state=&session=&peer=&with=&limit=&offset=
//
// The listing is the FILTER form of the permission table: the caller's own letters (sent or
// received) need nothing; another caller's letters appear only with the `.others` permission of
// their recipient's type; a job credential sees its own job's letters; a ceiling drops letters
// whose both ends are outside it.
//
// peer= / with= narrow it to ONE conversation (the letters between those two parties, whichever
// way they went, oldest first) for the conversation timeline of the console.
func (s *Server) handleMailList(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	box, ok := mailBoxQuery(c)
	if !ok {
		return
	}
	convKey, ok := s.mailConversationQuery(c)
	if !ok {
		return
	}
	def := mailListDefault
	if convKey != "" {
		def = mailListMax // a conversation comes whole unless the caller pages it itself
	}
	limit, offset := mailPageQuery(c, def, mailListMax)
	var states []string
	for _, st := range strings.Split(c.Query("state"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			states = append(states, st)
		}
	}
	thread := strings.TrimSpace(c.Query("thread"))
	session := strings.TrimSpace(c.Query("session"))
	opts := jobstore.ListMailOpts{ThreadID: thread, States: states, Limit: jobstore.MailListMaxLimit, OldestFirst: thread != "" || convKey != ""}
	if session != "" {
		// A session filter narrows the store query when the box fixes the side.
		switch box {
		case "in":
			opts.ToKind, opts.ToID = jobstore.MailKindSession, session
		case "out":
			opts.FromKind, opts.FromID = jobstore.MailKindSession, session
		}
	}
	keep := s.mailListKeep(c, box, session)

	kept := make([]jobstore.Mail, 0, limit)
	skipped := 0
	for page := 0; page < mailListScanPages && len(kept) < limit; page++ {
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
			if convKey != "" && mailConversationKey(mailFromParty(m), mailToParty(m)) != convKey {
				continue
			}
			if skipped < offset {
				skipped++
				continue
			}
			kept = append(kept, m)
			if len(kept) >= limit {
				break
			}
		}
		if len(rows) < jobstore.MailListMaxLimit {
			break
		}
	}
	ps := s.newMailPersonas(kept)
	result := make([]mailView, 0, len(kept))
	// A broadcast's addressee table is read once per group, however many copies the page shows.
	broadcasts := map[int64]*mailBroadcastView{}
	for _, m := range kept {
		v := toMailView(m)
		ps.forView(&v)
		if m.BroadcastID > 0 {
			if sum, ok := broadcasts[m.BroadcastID]; ok {
				v.Broadcast = sum
			} else if sum, err := s.mailBroadcastSummary(m); err == nil {
				broadcasts[m.BroadcastID] = sum
				v.Broadcast = sum
			}
		}
		result = append(result, v)
	}
	c.JSON(http.StatusOK, map[string]any{"mail": result, "count": len(result)})
}

// mailListKeep is the keep predicate of GET /v1/mail for one box (and one optional session).
// The conversation aggregation filters with this very predicate, so a conversation counts
// exactly the letters the listing shows — never one the caller may not see.
func (s *Server) mailListKeep(c *rux.Context, box, session string) func(jobstore.Mail) bool {
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
	return func(m jobstore.Mail) bool {
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

type mailRedactReq struct {
	Literals []string `json:"literals"`
	Patterns []string `json:"patterns"`
}

// mayRedactMail decides whether the caller may erase secrets from a letter (M8, 设计「敏感信息」):
// the letter's own two sides — the caller that sent it (from_caller) and the owner of its
// recipient — or a holder of job.delete.others, which is the permission job redaction names
// ("权限与 job redact 一致").
func (s *Server) mayRedactMail(c *rux.Context, m jobstore.Mail) bool {
	caller := callerFromCtx(c)
	if m.FromCaller != "" && m.FromCaller == caller {
		return true
	}
	if s.mailIsRecipientSide(caller, m) {
		return true
	}
	return s.can(c, authz.PermJobDeleteOthers)
}

// POST /v1/mail/{id}/redact {literals, patterns} — erase secrets from one letter's body and hold
// reason, and from the collection-box summaries that quote it. The answer is aggregate-only by
// contract (counts, never a literal or a matched value), exactly like job redaction.
func (s *Server) handleMailRedact(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	if _, isJob := jobCallerFromCtx(c); isJob {
		writeError(c, http.StatusForbidden, "mail not permitted", "a job credential may not redact a letter")
		return
	}
	m, ok := s.requireMailAccess(c, true)
	if !ok {
		return
	}
	if !s.mayRedactMail(c, m) {
		s.writePermissionDenied(c, authz.PermJobDeleteOthers)
		return
	}
	var req mailRedactReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid redact request", "body must contain literals and/or patterns")
		return
	}
	report, err := s.jobs.Meta().RedactMail(m.ID, req.Literals, req.Patterns)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, jobstore.ErrMailNotFound) || errors.Is(err, jobstore.ErrMailInvalid) {
			status = http.StatusNotFound
		}
		writeError(c, status, "mail redaction failed", redactSafeError(err))
		return
	}
	c.JSON(http.StatusOK, report)
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
