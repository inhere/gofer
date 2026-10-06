package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

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
	steward := callerIsSteward(c)
	in := work.AskInput{SessionID: body.SessionID, Text: body.Text, WorkID: body.WorkID, By: workBy(c)}
	if steward {
		in.Prefix = stewardAskPrefix
		in.Allow = func(string, bool) bool { return true } // it speaks for the person who enabled it
	} else {
		in.Allow = func(sid string, isJob bool) bool {
			if isJob {
				return callerKindFromCtx(c) == callerKindUser
			}
			return s.sessionMayAnswerQuiet(c, sid)
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
