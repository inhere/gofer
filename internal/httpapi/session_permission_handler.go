package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/sessionrelay"
)

// Terminal permission prompts mirrored on the web (Claude Code PermissionRequest):
//
//	POST /v1/sessions/{sid}/permissions              the hook opens the prompt it waits on
//	POST /v1/sessions/{sid}/permissions/resolve      the hook reports it was settled in the terminal
//	POST /v1/sessions/{sid}/permissions/{id}/answer  the person answers allow | always:<i> | deny[:<reason>]
//
// The hook polls the opened decision through GET /v1/sessions/{sid}/turns/{id}
// (same long-poll, same release rules as a relay turn).

type openPermissionReq struct {
	ToolName    string                              `json:"tool_name"`
	Summary     string                              `json:"summary"`
	Input       string                              `json:"input,omitempty"`
	Suggestions []sessionrelay.PermissionSuggestion `json:"suggestions,omitempty"`
	Fingerprint string                              `json:"fp,omitempty"`
	TimeoutSec  int64                               `json:"timeout_sec,omitempty"`
}

type resolvePermissionReq struct {
	Fingerprint string `json:"fp,omitempty"`
}

type answerPermissionReq struct {
	Answer string `json:"answer"`
}

// permissionCallerIsOwner gates every permission endpoint on the PERSON who owns
// the terminal: a tool permission is the most direct "act for this human" there is,
// so — stricter than sessionMayAnswer — no worker or job credential (the steward
// included) and no can_answer delegation: only the caller the session registered
// with, or anyone on an owner-less session (empty-token server / pre-owner row).
func (s *Server) permissionCallerIsOwner(c *rux.Context, sid, action string) bool {
	if kind := callerKindFromCtx(c); kind != callerKindUser {
		writeError(c, http.StatusForbidden, action+" not permitted for this caller",
			"only the person who owns the terminal session answers its permission prompts ("+kind+" credentials cannot)")
		return false
	}
	a, err := s.relay.Session(sid)
	if err != nil {
		writeError(c, relayStatus(err), action+" failed", err.Error())
		return false
	}
	if by := callerFromCtx(c); a.CallerID != "" && a.CallerID != by {
		writeError(c, http.StatusForbidden, action+" not permitted for this caller",
			"the session belongs to caller "+a.CallerID)
		return false
	}
	return true
}

func (s *Server) handleOpenSessionPermission(c *rux.Context) {
	if !s.relayReady(c) || !s.permissionCallerIsOwner(c, c.Param("sid"), "open permission") {
		return
	}
	var body openPermissionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	d, err := s.relay.OpenPermission(c.Param("sid"), sessionrelay.PermissionInput{
		PermissionDetail: sessionrelay.PermissionDetail{
			ToolName: body.ToolName, Summary: body.Summary, Input: body.Input,
			Suggestions: body.Suggestions, Fingerprint: body.Fingerprint,
		},
		TimeoutSec: body.TimeoutSec,
	})
	if err != nil {
		writeError(c, relayStatus(err), "open permission failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}

func (s *Server) handleResolveSessionPermission(c *rux.Context) {
	if !s.relayReady(c) || !s.permissionCallerIsOwner(c, c.Param("sid"), "resolve permission") {
		return
	}
	var body resolvePermissionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	n, err := s.relay.ResolvePermissions(c.Param("sid"), body.Fingerprint)
	if err != nil {
		writeError(c, relayStatus(err), "resolve permission failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"resolved": n})
}

func (s *Server) handleAnswerSessionPermission(c *rux.Context) {
	if !s.relayReady(c) || !s.permissionCallerIsOwner(c, c.Param("sid"), "answer permission") {
		return
	}
	var body answerPermissionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	d, err := s.relay.AnswerPermission(c.Param("sid"), c.Param("id"), body.Answer, decisionAnswerer(c))
	if err != nil {
		writeError(c, relayStatus(err), "answer permission failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toDecisionView(d))
}
