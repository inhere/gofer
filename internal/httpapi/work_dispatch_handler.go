package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

type workDispatchReq struct {
	To           string `json:"to"`
	SessionID    string `json:"session_id"`
	JobID        string `json:"job_id"`
	PlanID       string `json:"plan_id"`
	NewPlanTitle string `json:"new_plan_title"`
}

// POST /v1/work-items/{id}/dispatch {to, session_id?, job_id?, plan_id?, new_plan_title?}:
// hand a captured item over. to=session sends its text through the web-message path;
// to=job links an already-submitted job (the Web NewJob page submits first); to=todo
// reuses to-todo; archive / drop close it. Only a captured item can be dispatched (409
// otherwise); a failed hand-over leaves it captured. A job credential and a worker token
// are refused.
func (s *Server) handleWorkDispatch(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	if callerKindFromCtx(c) == callerKindJob {
		writeError(c, http.StatusForbidden, "dispatch not permitted for this caller",
			"a job credential may not dispatch a captured work item")
		return
	}
	var body workDispatchReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	to := strings.TrimSpace(body.To)
	id := c.Param("id")
	by := workBy(c)
	switch to {
	case work.DispatchSession, work.DispatchJob, work.DispatchTodo, work.DispatchArchive, work.DispatchDrop:
	case "issue":
		writeError(c, http.StatusBadRequest, "dispatch failed", "to=issue is not supported yet")
		return
	default:
		writeError(c, http.StatusBadRequest, "dispatch failed", "to must be one of session|job|todo|archive|drop")
		return
	}
	item, err := s.work.Captured(id)
	if err != nil {
		s.writeDispatchError(c, err)
		return
	}
	switch to {
	case work.DispatchArchive, work.DispatchDrop:
		if _, err := s.work.DispatchClose(id, to, by); err != nil {
			s.writeDispatchError(c, err)
			return
		}
	case work.DispatchSession:
		if !s.dispatchToSession(c, item, strings.TrimSpace(body.SessionID), by) {
			return
		}
	case work.DispatchJob:
		if !s.dispatchToJob(c, item, strings.TrimSpace(body.JobID), by) {
			return
		}
	case work.DispatchTodo:
		if !s.dispatchToTodo(c, item, body, by) {
			return
		}
	}
	s.respondWorkDetail(c, item.Item.ID, http.StatusOK)
}

func (s *Server) writeDispatchError(c *rux.Context, err error) {
	if errors.Is(err, work.ErrNotCaptured) {
		writeError(c, http.StatusConflict, "dispatch failed", err.Error())
		return
	}
	writeWorkError(c, err, "dispatch work item")
}

func (s *Server) dispatchToSession(c *rux.Context, item work.CapturedItem, sid, by string) bool {
	if sid == "" {
		writeError(c, http.StatusBadRequest, "dispatch failed", "session_id is required for to=session")
		return false
	}
	if !s.relayReady(c) {
		return false
	}
	a, err := s.relay.Session(sid)
	if err != nil {
		writeError(c, relayStatus(err), "dispatch failed", err.Error())
		return false
	}
	if !s.mayReplyToSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReplyOthers)
		return false
	}
	m, err := s.relay.SendMessage(c.Req.Context(), sid, item.Text, callerFromCtx(c))
	if err == nil && (m.Status == jobstore.SessionMessageFailed || m.Status == jobstore.SessionMessageUndelivered) {
		err = errors.New(m.Error)
	}
	if err != nil {
		status := relayStatus(err)
		if m.ID != "" || status == http.StatusInternalServerError {
			status = http.StatusConflict
		}
		writeError(c, status, "dispatch to session failed", "the item stays in the capture box: "+err.Error())
		return false
	}
	s.relay.FollowWeb(sid, callerFromCtx(c))
	if _, err := s.work.FinishDispatch(item.Item.ID, jobstore.WorkActive, "", "",
		"已送出到会话 "+sid, by); err != nil {
		s.writeDispatchError(c, err)
		return false
	}
	return true
}

func (s *Server) dispatchToJob(c *rux.Context, item work.CapturedItem, jobID, by string) bool {
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "dispatch failed", "job_id is required for to=job")
		return false
	}
	res, ok := s.jobForAccess(jobID)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+jobID)
		return false
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return false
	}
	if _, err := s.work.FinishDispatch(item.Item.ID, jobstore.WorkActive, jobstore.WorkLinkJob, jobID,
		fmt.Sprintf("已关联 job %s（%s）", jobID, res.Status), by); err != nil {
		s.writeDispatchError(c, err)
		return false
	}
	// The job may have ended before it was linked: its terminal hook (wireWorkJobOutcomes,
	// which also writes the line for a job that ends AFTER the link) found no item then.
	if now, ok := s.jobForAccess(jobID); ok && job.IsTerminal(now.Status) {
		s.work.NoteJobOutcome(workJobOutcome(now))
	}
	return true
}

func (s *Server) dispatchToTodo(c *rux.Context, item work.CapturedItem, body workDispatchReq, by string) bool {
	// Same plan.manage.others rule as POST /v1/work-items/{id}/to-todo.
	if body.PlanID != "" {
		if p, ok, err := s.jobs.Meta().GetPlan(body.PlanID); err != nil {
			writeError(c, http.StatusInternalServerError, "get plan failed", err.Error())
			return false
		} else if ok && !s.mayManagePlan(c, p.Owner) {
			s.denyPlanAction(c, authz.PermPlanManageOthers)
			return false
		}
	}
	_, _, err := s.work.DispatchToTodo(item.Item.ID, work.ToTodoInput{PlanID: body.PlanID, NewPlanTitle: body.NewPlanTitle, By: by})
	var dup *work.AlreadyTodoError
	switch {
	case errors.As(err, &dup):
		c.JSON(http.StatusConflict, map[string]any{
			"error": "work item already linked to a todo", "detail": dup.Error(),
			"todo_id": dup.TodoID, "plan_id": dup.PlanID,
		})
		return false
	case errors.Is(err, work.ErrPlanNotFound):
		writeError(c, http.StatusNotFound, "dispatch to todo failed", err.Error())
		return false
	case err != nil:
		s.writeDispatchError(c, err)
		return false
	}
	return true
}
