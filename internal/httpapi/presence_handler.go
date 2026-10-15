package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/presence"
)

// registerAgentReq is the POST /v1/agents/register body. caller_id / client are
// NOT read from the body — they are stamped server-side (auth caller + client IP,
// anti-spoof, mirrors job submit).
type registerAgentReq struct {
	Name    string `json:"name"`
	Role    string `json:"role,omitempty"`
	Project string `json:"project,omitempty"`
}

// pollInboxReq carries the agent_token presented for the soft-isolation check.
type pollInboxReq struct {
	AgentToken string `json:"agent_token"`
}

// postMessageReq is the POST /v1/messages body. from_agent is the sender's own
// agent_id (within the same token trust domain; soft isolation only).
type postMessageReq struct {
	FromAgent string `json:"from_agent"`
	To        string `json:"to"`
	Kind      string `json:"kind"`
	Body      string `json:"body,omitempty"`
	Ref       string `json:"ref,omitempty"`
}

// deregisterReq carries the agent_token for the deregister authorisation check.
type deregisterReq struct {
	AgentToken string `json:"agent_token"`
}

// handleRegisterAgent registers/renews a driver agent and returns {agent_id,
// agent_token}. caller_id/client are stamped from the auth context + client IP
// (provenance, E34); the body only carries self-reported name/role/project.
func (s *Server) handleRegisterAgent(c *rux.Context) {
	var req registerAgentReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	res, err := s.presence.Register(presence.RegisterInput{
		Name:       req.Name,
		Role:       req.Role,
		ProjectKey: req.Project,
		CallerID:   callerFromCtx(c),
		Client:     clientIP(c),
	})
	if err != nil {
		writeError(c, presenceStatus(err), "register agent failed", err.Error())
		return
	}
	// res is presence.RegisterResult (snake_case json: agent_id/agent_token).
	c.JSON(http.StatusOK, res)
}

// handleListPresence returns the online registry (?role=/?project= filters). The
// agent_token is never included (presence.Agent has no token field).
func (s *Server) handleListPresence(c *rux.Context) {
	list, err := s.presence.List(c.Query("role"), c.Query("project"))
	if err != nil {
		writeError(c, presenceStatus(err), "list presence failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"agents": list})
}

// handlePollInbox returns the agent's unread messages, refreshing its heartbeat.
// The agent_token is verified (403 on mismatch, 404 on unknown). ?ack=false peeks
// without consuming; the default consumes (marks read).
func (s *Server) handlePollInbox(c *rux.Context) {
	id := c.Param("id")
	var req pollInboxReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	ack := c.Query("ack") != "false" && c.Query("ack") != "0"
	msgs, err := s.presence.Poll(id, req.AgentToken, ack)
	if err != nil {
		writeError(c, presenceStatus(err), "poll inbox failed", err.Error())
		return
	}
	if msgs == nil {
		msgs = []presence.Message{}
	}
	c.JSON(http.StatusOK, map[string]any{"messages": msgs})
}

// handleListInbox is the P5 read-only inbox view (GET /v1/agents/{id}/inbox): it lists
// the agent's messages WITHOUT consuming them or refreshing its heartbeat — for
// observing escalation backlog, distinct from POST .../inbox/poll (which acks + touches
// last_seen). ?include_read=1 also returns已读 messages. No token (internal /inner read);
// an unknown agent_id yields an empty list.
//
// It is the domain's one InHandler route (plan T1.5i): the mailbox belongs to the caller that
// registered the agent, anyone else needs session.read.others (mayReadAgentInbox).
func (s *Server) handleListInbox(c *rux.Context) {
	id := c.Param("id")
	if !s.mayReadAgentInbox(c, id) {
		s.denySessionAction(c, authz.PermSessionReadOthers)
		return
	}
	includeRead := c.Query("include_read") == "1" || c.Query("include_read") == "true"
	msgs, err := s.presence.ListInbox(id, includeRead)
	if err != nil {
		writeError(c, presenceStatus(err), "list inbox failed", err.Error())
		return
	}
	if msgs == nil {
		msgs = []presence.Message{}
	}
	c.JSON(http.StatusOK, map[string]any{"messages": msgs})
}

// mayReadAgentInbox is design `InHandler(session.read.others, deny)` for the read-only inbox
// view of one driver agent: design §1's `session.read.others` row covers 「他人…收件箱」, the
// only 收件箱 of the router, so the caller that REGISTERED the agent reads its mailbox without
// any permission (presence stamps the authenticating caller id at register time, and never a
// value the request supplied) and every other caller needs session.read.others — which is
// 种入, so a deployment that keeps the seed is unchanged.
//
// An agent id that is unknown (or a server whose store cannot answer) has no owner to compare
// against, so it takes the ownerless branch of the session rules — any user-class caller — and
// the empty list the handler already answers. A worker token and a job credential are not
// governed by permissions (design §2「只对 user 类调用方计算」): the pre-migration route checked
// nothing at all, so both keep the whole read surface (nonUserUnchanged), exactly like the
// session reads of authz_session_rules.go.
func (s *Server) mayReadAgentInbox(c *rux.Context, agentID string) bool {
	return s.sessionAccess(c, s.agentInboxOwner(agentID), nil, []string{authz.PermSessionReadOthers}, nonUserUnchanged)
}

// agentInboxOwner is the caller id presence stamped when the agent registered: the mailbox is
// the agent's, and an agent belongs to the person that registered it. An unknown id, a server
// without a store and a row without a caller id all answer "" — nobody can be shown to own it
// (which the rule above reads as ownerless).
func (s *Server) agentInboxOwner(agentID string) string {
	if s.jobs == nil || s.jobs.Meta() == nil {
		return ""
	}
	rec, ok, err := s.jobs.Meta().GetPresence(agentID)
	if err != nil || !ok {
		return ""
	}
	return rec.CallerID
}

// handlePostMessage delivers a message (direct / role: / role-one: / broadcast) and
// returns {delivered} — the number of inbox rows created by the fan-out.
func (s *Server) handlePostMessage(c *rux.Context) {
	var req postMessageReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	n, err := s.presence.Post(req.FromAgent, req.To, req.Kind, req.Body, req.Ref)
	if err != nil {
		writeError(c, presenceStatus(err), "post message failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"delivered": n})
}

// handleDeregister actively removes an agent (token-checked, idempotent).
func (s *Server) handleDeregister(c *rux.Context) {
	id := c.Param("id")
	var req deregisterReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if err := s.presence.Deregister(id, req.AgentToken); err != nil {
		writeError(c, presenceStatus(err), "deregister failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// presenceStatus maps a presence-package error to an HTTP status (mirrors
// interactionStatus): token mismatch -> 403; unknown agent -> 404; validation /
// anything else -> 400.
func presenceStatus(err error) int {
	switch {
	case errors.Is(err, presence.ErrUnauthorizedAgent):
		return http.StatusForbidden
	case errors.Is(err, presence.ErrUnknownAgent):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}
