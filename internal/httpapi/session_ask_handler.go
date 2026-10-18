package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/work"
)

// stewardAskPrefix tells the receiving session who is talking.
const stewardAskPrefix = "[gofer 管家带话]"

// POST /v1/session-ask {session_id, text, work_id?} — "带话": a short message for a
// running session ("the resource arrived, carry on"), over the existing 传话 channels
// (terminal session → relay / messenger, ACP / pty session job → `job say`). A session
// that is not online is an explicit 409 and nothing is queued. The delivery is logged on
// the session's work item(s) under the speaker's label. Open to a person and to the
// steward credential only (the generic job credential is default-denied by the
// middleware).
//
// 说话入口按目标分流 (design §1): the target decides the permission — a terminal session is
// session.reply.others, a session job is job.manage.others — so one action never answers to
// two permissions.
func (s *Server) handleSessionAsk(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) || !s.relayReady(c) {
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
		WorkID    string `json:"work_id"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if !s.requireResourceScope(c, "session", strings.TrimSpace(body.SessionID)) ||
		(strings.TrimSpace(body.WorkID) != "" && !s.requireResourceScope(c, "work_item", strings.TrimSpace(body.WorkID))) {
		return
	}
	steward := callerIsSteward(c)
	in := work.AskInput{SessionID: body.SessionID, Text: body.Text, WorkID: body.WorkID, By: workBy(c)}
	if steward {
		in.Prefix = stewardAskPrefix
		in.Allow = func(string, bool) bool { return true } // it speaks for the person who enabled it
	} else {
		// 说话入口按目标分流 (design §1): a TERMINAL session follows session.reply.others, a
		// session JOB (ACP / pty) is 对会话 job 说话 and follows job.manage.others. The job
		// branch is only reached by a person (workNotAWorker refused a worker above, and a
		// member / leader job credential is default-denied by SEC-01).
		in.Allow = func(sid string, isJob bool) bool {
			if isJob {
				return s.can(c, authz.PermJobManageOthers)
			}
			owner, ok := s.terminalSessionOwner(sid)
			if !ok {
				// The service answers 404 for an id it cannot resolve either; nobody can be
				// shown to own it, so only the permission would answer — fail closed.
				return s.can(c, authz.PermSessionReplyOthers)
			}
			return s.mayReplyToSession(c, owner)
		}
		// The named target is decided HERE as well, so the refusal names the permission
		// (design §3「403 响应写明缺哪个权限」) instead of surfacing the ledger's own line.
		// The ledger applies in.Allow again for every session it walks.
		if primary := strings.TrimSpace(body.SessionID); primary != "" {
			if s.work.IsJobSession(primary) {
				if !s.can(c, authz.PermJobManageOthers) {
					s.denySessionAction(c, authz.PermJobManageOthers)
					return
				}
			} else if owner, ok := s.terminalSessionOwner(primary); ok && !s.mayReplyToSession(c, owner) {
				s.denySessionAction(c, authz.PermSessionReplyOthers)
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(c.Req.Context(), 2*time.Minute)
	defer cancel()
	res, err := s.work.AskSession(ctx, in)
	if err != nil {
		writeSessionAskError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func writeSessionAskError(c *rux.Context, err error) {
	switch {
	case errors.Is(err, work.ErrAskNotFound):
		writeError(c, http.StatusNotFound, "session ask failed", err.Error())
	case errors.Is(err, work.ErrAskOffline):
		writeError(c, http.StatusConflict, "session ask failed", err.Error())
	case errors.Is(err, work.ErrAskDenied):
		writeError(c, http.StatusForbidden, "session ask failed", err.Error())
	case errors.Is(err, work.ErrAskEmpty):
		writeError(c, http.StatusBadRequest, "session ask failed", err.Error())
	case errors.Is(err, work.ErrAskUnsupported):
		writeError(c, http.StatusServiceUnavailable, "session ask failed", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "session ask failed", strings.TrimSpace(err.Error()))
	}
}
