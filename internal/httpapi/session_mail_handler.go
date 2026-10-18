package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// The hook's mail take (设计「投递语义」, plan M4b): the session's own SessionStart /
// UserPromptSubmit / PreToolUse / Stop hooks take the letters addressed to the session in
// two steps — claim (pending → delivering with a lease), then confirm (delivering →
// delivered) — and inject only what the confirmation returned: confirm before inject, so a
// letter is injected AT MOST once. The Stop path with a relay turn open uses one call that
// closes the turn and takes the letters together.

// Default and ceiling of one take (设计「默认数值」: 单次注入 5 封 / 8000 字).
const (
	DefaultMailTakeLimit = 5
	DefaultMailTakeChars = 8000
	maxMailTakeLimit     = 20
	maxMailTakeChars     = 32000
)

type mailTakeReq struct {
	Limit      int  `json:"limit,omitempty"`
	MaxChars   int  `json:"max_chars,omitempty"`
	UrgentOnly bool `json:"urgent_only,omitempty"`
}

func (r mailTakeReq) opts() jobstore.TakeMailOpts {
	o := jobstore.TakeMailOpts{Limit: r.Limit, MaxRunes: r.MaxChars, UrgentOnly: r.UrgentOnly}
	if o.Limit <= 0 {
		o.Limit = DefaultMailTakeLimit
	}
	if o.Limit > maxMailTakeLimit {
		o.Limit = maxMailTakeLimit
	}
	if o.MaxRunes <= 0 {
		o.MaxRunes = DefaultMailTakeChars
	}
	if o.MaxRunes > maxMailTakeChars {
		o.MaxRunes = maxMailTakeChars
	}
	return o
}

// takenMailView is one letter as the hook renders it into a <gofer-mail> block.
type takenMailView struct {
	ID        int64         `json:"id"`
	ThreadID  string        `json:"thread_id"`
	From      mailPartyView `json:"from"`
	Topic     string        `json:"topic,omitempty"`
	Urgent    bool          `json:"urgent,omitempty"`
	Body      string        `json:"body"`
	Truncated bool          `json:"truncated,omitempty"`
	CreatedAt int64         `json:"created_at"`
}

func (s *Server) takenMailViews(in []jobstore.TakenMail) []takenMailView {
	out := make([]takenMailView, 0, len(in))
	for _, t := range in {
		v := takenMailView{
			ID: t.ID, ThreadID: t.ThreadID,
			From:   mailPartyView{Kind: t.FromKind, ID: t.FromID, Caller: t.FromCaller, Display: mail.OriginOf(t.Mail).Display()},
			Urgent: t.Urgent, Body: t.Body, Truncated: t.Truncated, CreatedAt: t.CreatedAt,
		}
		// The thread's topic is what the SENDER says it works on; a caller or job sender has none.
		if t.FromKind == jobstore.MailKindSession && s.relay != nil {
			if a, err := s.relay.Session(t.FromID); err == nil {
				v.Topic = a.Topic
			}
		}
		out = append(out, v)
	}
	return out
}

// mailTakeAccess resolves the session and applies the session rule (own session: nothing;
// someone else's: session.reply.others). A job credential and a worker never reach these
// routes (they declare no JobCred / WorkerSelf).
func (s *Server) mailTakeAccess(c *rux.Context, action string) (jobstore.AgentSession, bool) {
	if !s.relayReady(c) {
		return jobstore.AgentSession{}, false
	}
	if s.jobs == nil || s.jobs.Meta() == nil {
		writeError(c, http.StatusServiceUnavailable, "mailbox unavailable", "no job store wired on this server")
		return jobstore.AgentSession{}, false
	}
	a, ok := s.sessionForAccess(c, action)
	if !ok {
		return a, false
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return a, false
	}
	return a, true
}

// handleClaimSessionMail is POST /v1/sessions/{sid}/mail/claim.
func (s *Server) handleClaimSessionMail(c *rux.Context) {
	if _, ok := s.mailTakeAccess(c, "claim session mail"); !ok {
		return
	}
	var body mailTakeReq
	if c.Req.ContentLength != 0 {
		if err := c.BindJSON(&body); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	taken, remaining, err := s.jobs.Meta().ClaimSessionMail(c.Param("sid"), body.opts())
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"mails": s.takenMailViews(taken), "remaining": remaining})
}

// handleConfirmSessionMail is POST /v1/sessions/{sid}/mail/confirm: the hook's confirmation
// that it is about to inject the claimed letters. Only ids returned in `confirmed` may be
// injected.
func (s *Server) handleConfirmSessionMail(c *rux.Context) {
	if _, ok := s.mailTakeAccess(c, "confirm session mail"); !ok {
		return
	}
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	done, err := s.jobs.Meta().ConfirmSessionMail(c.Param("sid"), body.IDs)
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	ids := make([]int64, 0, len(done))
	for _, m := range done {
		ids = append(ids, m.ID)
	}
	c.JSON(http.StatusOK, map[string]any{"confirmed": ids})
}

// handleCompleteMailTurn is POST /v1/sessions/{sid}/turns/{id}/complete-mail: Stop delivery
// with a relay turn open. completed=true carries the already-confirmed letters (the turn is
// closed); completed=false means a web answer won the race or nothing was pending.
func (s *Server) handleCompleteMailTurn(c *rux.Context) {
	if _, ok := s.mailTakeAccess(c, "complete mail turn"); !ok {
		return
	}
	var body mailTakeReq
	if c.Req.ContentLength != 0 {
		if err := c.BindJSON(&body); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	completed, taken, err := s.relay.CompleteMailTurn(c.Param("sid"), c.Param("id"), body.opts())
	if err != nil {
		writeError(c, relayStatus(err), "complete mail turn failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"completed": completed, "mails": s.takenMailViews(taken)})
}

// mailPendingOf is the session's letters still to read (the heartbeat's mail_pending).
func (s *Server) mailPendingOf(sid string) int {
	if s.jobs == nil || s.jobs.Meta() == nil {
		return 0
	}
	n, err := s.jobs.Meta().PendingCount(jobstore.MailKindSession, sid)
	if err != nil {
		return 0
	}
	return n
}
