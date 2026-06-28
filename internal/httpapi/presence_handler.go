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

// registerAgentResp returns the public address + private capability handle. The
// agent_token is returned ONLY here (never by presence listing).
type registerAgentResp struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

// presenceAgentView is the snake_case projection of one online agent (no token).
type presenceAgentView struct {
	AgentID    string `json:"agent_id"`
	Name       string `json:"name"`
	Role       string `json:"role,omitempty"`
	ProjectKey string `json:"project_key,omitempty"`
	Client     string `json:"client,omitempty"`
	Status     string `json:"status"`
	LastSeenAt int64  `json:"last_seen_at"`
}

// pollInboxReq carries the agent_token presented for the soft-isolation check.
type pollInboxReq struct {
	AgentToken string `json:"agent_token"`
}

// messageView is the snake_case projection of one inbox message.
type messageView struct {
	ID        string `json:"id"`
	FromAgent string `json:"from_agent"`
	ToSpec    string `json:"to_spec,omitempty"`
	Kind      string `json:"kind"`
	Body      string `json:"body,omitempty"`
	Ref       string `json:"ref,omitempty"`
	CreatedAt int64  `json:"created_at"`
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
	c.JSON(http.StatusOK, registerAgentResp{AgentID: res.AgentID, AgentToken: res.AgentToken})
}

// handleListPresence returns the online registry (?role=/?project= filters). The
// agent_token is never included.
func (s *Server) handleListPresence(c *rux.Context) {
	list, err := s.presence.List(c.Query("role"), c.Query("project"))
	if err != nil {
		writeError(c, presenceStatus(err), "list presence failed", err.Error())
		return
	}
	views := make([]presenceAgentView, 0, len(list))
	for _, a := range list {
		views = append(views, presenceAgentView{
			AgentID:    a.AgentID,
			Name:       a.Name,
			Role:       a.Role,
			ProjectKey: a.ProjectKey,
			Client:     a.Client,
			Status:     a.Status,
			LastSeenAt: a.LastSeenAt,
		})
	}
	c.JSON(http.StatusOK, map[string]any{"agents": views})
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
	views := make([]messageView, 0, len(msgs))
	for _, m := range msgs {
		views = append(views, messageView{
			ID:        m.ID,
			FromAgent: m.FromAgent,
			ToSpec:    m.ToSpec,
			Kind:      m.Kind,
			Body:      m.Body,
			Ref:       m.Ref,
			CreatedAt: m.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, map[string]any{"messages": views})
}

// handlePostMessage delivers a message (direct / role: / broadcast) and returns
// {delivered} — the number of inbox rows created by the fan-out.
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
