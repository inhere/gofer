package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/sessionrelay"
)

// handleSessionTakeoverPlan is the dry run of a wake-up (GET
// /v1/sessions/{sid}/takeover-plan): can a new process continue this session, and
// if not, why — in plain Chinese. The answer is always 200 for a known session;
// "cannot" is data (can=false + reason), not an error.
func (s *Server) handleSessionTakeoverPlan(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	plan, err := s.relay.PlanResume(c.Param("sid"))
	if err != nil {
		writeError(c, relayStatus(err), "takeover plan failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, plan)
}

type sessionResumeReq struct {
	// InitialInput is optional: the text typed into the new terminal once it is up.
	InitialInput string `json:"initial_input,omitempty"`
}

// handleSessionResume wakes a session up (POST /v1/sessions/{sid}/resume): it
// starts an interactive `--resume` process for it — also when the session has
// ended — and answers with the new job to attach to. The error's short string
// carries the reason code ("resume failed: no_resume_template"); detail is a plain
// Chinese explanation. Only a human caller may do this, like deliver.
func (s *Server) handleSessionResume(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if !s.sessionMayAnswer(c, c.Param("sid"), "resume") {
		return
	}
	var body sessionResumeReq
	if c.Req.ContentLength != 0 {
		if err := c.BindJSON(&body); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	res, err := s.relay.Resume(c.Req.Context(), c.Param("sid"), body.InitialInput, callerFromCtx(c))
	if err != nil {
		if reason := sessionrelay.DeliverReason(err); reason != "" {
			detail := err.Error()
			var ue *sessionrelay.UndeliverableError
			if errors.As(err, &ue) && ue.Err != nil {
				detail = ue.Err.Error()
			}
			writeError(c, deliverStatus(err), "resume failed: "+reason, detail)
			return
		}
		writeError(c, relayStatus(err), "resume failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"path": res.Path, "job_id": res.JobID, "decision_id": res.DecisionID})
}
