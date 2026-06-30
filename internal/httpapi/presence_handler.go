package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

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
func (s *Server) handleListInbox(c *rux.Context) {
	id := c.Param("id")
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
