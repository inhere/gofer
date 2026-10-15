package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
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
	// Questions turns the card into an AskUserQuestion relayed to the web (gofer-02fu).
	Questions []sessionrelay.Question `json:"questions,omitempty"`
}

type resolvePermissionReq struct {
	Fingerprint string `json:"fp,omitempty"`
}

type answerPermissionReq struct {
	Answer string `json:"answer"`
}

// permissionCallerIsOwner gates the two endpoints the HOOK drives (open and resolve) on the
// PERSON the terminal is for: a tool permission is the most direct "act for this human"
// there is, so — stricter than the pre-migration answer gate was — no worker or job credential (the
// steward included) and no can_answer delegation: only the caller the session registered
// with, anyone on an owner-less session (empty-token server / pre-owner row), or a root
// administrator (can_admin).
//
// Answering the prompt is the THIRD endpoint and is declared on its own permission —
// InHandler(session.permission.answer), see mayAnswerSessionPermission (design §5): 打开与
// 收回提示仍只认会话 owner（那是 hook 自己的动作），作答可以由持有 session.permission.answer
// 的调用方完成，默认就是根管理员。
//
// DELIBERATE DIFFERENCE from design §5: the administrator arm below applies to open and
// resolve as well, where the design says 「打开（open）与收回（resolve）提示仍只认会话 owner」.
// The arm is a gofer-hxld addition ASKED FOR by the operator (2026-10-14): the session owner
// is the HOOK's identity (the agent the terminal runs as, e.g. agent-dev), so with per-caller
// tokens the person looking at the web console — a different caller id — could see the prompt
// and neither answer it nor settle it in the terminal. Answering someone else's prompt is
// exactly the kind of act an administrator already performs elsewhere (a takeover, a delete),
// so the arm is kept here — for open and resolve too — and the difference is recorded (plan
// T1.5c, design §5) instead of being narrowed away silently.
func (s *Server) permissionCallerIsOwner(c *rux.Context, sid, action string) bool {
	if kind := callerKindFromCtx(c); kind != callerKindUser {
		writeError(c, http.StatusForbidden, action+" not permitted for this caller",
			"only a person answers a terminal session's permission prompts ("+kind+" credentials cannot)")
		return false
	}
	a, err := s.relay.Session(sid)
	if err != nil {
		writeError(c, relayStatus(err), action+" failed", err.Error())
		return false
	}
	if by := callerFromCtx(c); a.CallerID != "" && a.CallerID != by && !s.callerIsRootAdmin(c) {
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
			Suggestions: body.Suggestions, Fingerprint: body.Fingerprint, Questions: body.Questions,
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

// A tool permission prompt is the most direct 「替这个人做事」 there is: the session's own
// owner (or anybody, on an ownerless session) answers it, and someone else's prompt needs
// session.permission.answer — which only a root administrator holds (design §5 / §3). The
// pre-migration rule asked for the can_admin bit; the permission is derived from exactly
// that bit, so the same callers answer as before.
func (s *Server) handleAnswerSessionPermission(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "answer permission")
	if !ok {
		return
	}
	if !s.mayAnswerSessionPermission(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionPermissionAnswer)
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
