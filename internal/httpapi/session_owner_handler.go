package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
)

// setSessionOwnerReq is the POST /v1/sessions/{sid}/owner body.
type setSessionOwnerReq struct {
	CallerID string `json:"caller_id"`
}

// handleSetSessionOwner hands a session to another authenticated caller
// (POST /v1/sessions/{sid}/owner, gofer-eh53).
//
// Why it exists: with several callers configured (server.callers) a terminal's
// hook may authenticate with a different caller than the one that registered its
// session — the operator splits the human's token from the agent's one and every
// hook on that machine changes identity at once. Ownership is decided at first
// contact and refusing a foreign report is right, but the session then freezes
// (state, last message, progress) with nothing to act on. The refusal is recorded
// on the session (`refused_caller_id`), the web shows it, and this endpoint is the
// way out: the owner becomes the caller the hooks now use, and their beats land
// again.
//
// The gate is `can_admin` (ServerConfig.CallerCanAdmin), NOT callerMayAdmin:
// governance.require_admin_capability waives the latter for every caller when it
// is off, and "give my session to your token" is exactly the act that must stay
// with the operator in a split-token setup. Worker credentials are refused too
// (a machine credential does not act for a person); job credentials are refused
// by jobCredentialMiddleware before the handler runs.
func (s *Server) handleSetSessionOwner(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "set session owner not permitted for this caller",
			"worker tokens cannot drive a terminal session: only a human caller with can_admin may hand one over")
		return
	}
	by := callerFromCtx(c)
	if !s.callerHoldsAdmin(by) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller",
			"transferring a session needs the can_admin capability (server.callers); governance.require_admin_capability does not open it")
		return
	}
	var body setSessionOwnerReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	target := strings.TrimSpace(body.CallerID)
	if target == "" {
		writeError(c, http.StatusBadRequest, "caller_id required", "a session must be handed to a named caller")
		return
	}
	if !s.sessionCallerKnown(target) {
		writeError(c, http.StatusBadRequest, "unknown caller",
			"no caller "+target+" on this server: list them in server.callers (an owner nobody can authenticate as would lock the session for everybody)")
		return
	}
	a, from, err := s.relay.SetSessionOwner(c.Param("sid"), target)
	if err != nil {
		writeError(c, relayStatus(err), "set session owner failed", err.Error())
		return
	}
	slog.Info("httpapi: session owner transferred", "session_id", a.SessionID,
		"from", from, "to", target, "by", by)
	c.JSON(http.StatusOK, s.toSessionView(a))
}

// callerHoldsAdmin reports whether caller carries the can_admin capability bit
// (ServerConfig.CallerCanAdmin). It is the gate for acting on ANOTHER caller's
// resource — the same shape as httpapi's "treat another caller's job" checks —
// and deliberately not callerMayAdmin: that one waives the capability whenever
// governance.require_admin_capability is off, which is fine for "the operator
// writes config" but would let any token take over any session.
func (s *Server) callerHoldsAdmin(caller string) bool {
	return s.cfg != nil && s.cfg.CallerCanAdmin(caller)
}

// sessionCallerKnown reports whether callerID is a caller this server can
// authenticate as it: a configured user caller, or "default" (the id the legacy
// single server token gets). Worker entries are machine credentials and never own
// a session, so they are not valid targets either.
func (s *Server) sessionCallerKnown(callerID string) bool {
	s.callerMu.RLock()
	defer s.callerMu.RUnlock()
	for _, ce := range s.callers {
		if ce.kind == callerKindUser && ce.id == callerID {
			return true
		}
	}
	return false
}
