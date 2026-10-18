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
	"github.com/inhere/gofer/internal/mail"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/work"
)

type workDispatchReq struct {
	To           string `json:"to"`
	SessionID    string `json:"session_id"`
	JobID        string `json:"job_id"`
	PlanID       string `json:"plan_id"`
	NewPlanTitle string `json:"new_plan_title"`
	TrackerID    string `json:"tracker_id"`
	Title        string `json:"title"`
}

// POST /v1/work-items/{id}/dispatch {to, session_id?, job_id?, plan_id?, new_plan_title?}:
// hand a captured item over. to=session sends a LETTER to that session through the mailbox
// (internal/mail) with the dispatcher as its origin — the rules of POST /v1/mail, which is
// also what records the item on the letter (capture_item_id); to=job links an already-submitted
// job (the Web NewJob page submits first); to=todo reuses to-todo; to=issue {tracker_id, title?}
// files a server-created issue in that repository's mirror (type bug for kind=issue, task
// otherwise), links it and parks the item (409 when the repository has no prefix yet);
// archive / drop close it. Only a captured item can be dispatched (409 otherwise); a failed
// hand-over leaves it captured. A job credential and a worker token are refused.
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
	case work.DispatchSession, work.DispatchJob, work.DispatchTodo, work.DispatchArchive, work.DispatchDrop, work.DispatchIssue:
	default:
		writeError(c, http.StatusBadRequest, "dispatch failed", "to must be one of session|job|todo|issue|archive|drop")
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
	case work.DispatchIssue:
		if !s.dispatchToIssue(c, item, body, by) {
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

// dispatchToSession hands a captured item to a session as a LETTER (M6, design 2026-10-16 §3
// 「交给会话」): the body is the item's text, the origin is the dispatcher (the session named by
// the session header, else the calling user — exactly what POST /v1/mail derives), the item is
// recorded on the letter (capture_item_id) and the letter's id goes into the item's journal.
// The permission table and the caller ceiling are the sender-side ones of POST /v1/mail
// (mailAuthorize), the limits are the mailbox's own (32 KB, rate, …).
//
// The session is resolved first, so the answer is about the ADDRESS rather than about a letter
// nobody can receive: an unknown session is a 404 and one that cannot take mail (ended) is a
// 409, both leaving the item in the capture box. A letter that still ends up held (the session
// went away in between) is a failure too — a parked letter is not a hand-over.
func (s *Server) dispatchToSession(c *rux.Context, item work.CapturedItem, sid, by string) bool {
	if sid == "" {
		writeError(c, http.StatusBadRequest, "dispatch failed", "session_id is required for to=session")
		return false
	}
	if !s.mailReady(c) || !s.relayReady(c) {
		return false
	}
	a, err := s.relay.Session(sid)
	if err != nil {
		writeError(c, relayStatus(err), "dispatch failed", err.Error())
		return false
	}
	if !sessionrelay.IsDeliverableState(a.State) {
		writeError(c, http.StatusConflict, "dispatch to session failed",
			"the item stays in the capture box: session "+sid+" is "+a.State)
		return false
	}
	org, ok := s.mailOrigin(c)
	if !ok {
		return false
	}
	res, err := s.mail.Send(mail.SendInput{
		To: "session:" + sid, Body: item.Text, Origin: org, CaptureItemID: item.Item.ID,
		Authorize: s.mailAuthorize(c, org, false),
	})
	if err != nil {
		s.writeMailError(c, err)
		return false
	}
	if res.Held {
		writeError(c, http.StatusConflict, "dispatch to session failed",
			"the item stays in the capture box: the letter could not be delivered to "+sid+
				" ("+res.Mail.LastError+")")
		return false
	}
	if _, err := s.work.FinishDispatch(item.Item.ID, jobstore.WorkActive, "", "",
		fmt.Sprintf("已送出到会话 %s（信件 #%d）", sid, res.Mail.ID), by); err != nil {
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

func (s *Server) dispatchToIssue(c *rux.Context, item work.CapturedItem, body workDispatchReq, by string) bool {
	if strings.TrimSpace(body.TrackerID) == "" {
		writeError(c, http.StatusBadRequest, "dispatch failed", "tracker_id is required for to=issue")
		return false
	}
	if s.trackerStore == nil {
		writeError(c, http.StatusServiceUnavailable, "tracker mirror unavailable", "")
		return false
	}
	_, _, err := s.work.DispatchToIssue(item.Item.ID, body.TrackerID, body.Title, by)
	switch {
	case err == nil:
		return true
	case errors.Is(err, jobstore.ErrTrackerNoPrefix), errors.Is(err, jobstore.ErrTrackerRepoUnknown), errors.Is(err, jobstore.ErrReservedTracker):
		writeTrackerCreateError(c, err)
	default:
		s.writeDispatchError(c, err)
	}
	return false
}
