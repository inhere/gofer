package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/workbench"
)

func (s *Server) handleGetWorkbenchThreadDiff(c *rux.Context) {
	// Reading a thread's diff is 读他人的 job (design §1 / plan T1.5c): someone else's thread
	// needs job.read.others, the caller's own needs nothing. A thread id the projection
	// cannot resolve is answered by the service, as before the migration.
	if owner, ok := s.workbenchThreadOwnerByID(c.Param("id")); ok && !s.mayReadOwnedRow(c, owner) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	result, err := s.workbench.Diff(c.Param("id"))
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, workbench.ErrInvalidThreadID):
			status = http.StatusBadRequest
		case errors.Is(err, workbench.ErrUnknownThread):
			status = http.StatusNotFound
		case errors.Is(err, workbench.ErrMissingDiffBase), errors.Is(err, workbench.ErrNotResumable):
			status = http.StatusConflict
		case errors.Is(err, workbench.ErrDiffTimeout):
			status = http.StatusGatewayTimeout
		case errors.Is(err, workbench.ErrUnavailable):
			status = http.StatusServiceUnavailable
		}
		writeError(c, status, "get workbench thread diff failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleReviewWorkbenchThread(c *rux.Context) {
	if !workbenchUserCaller(c) {
		writeError(c, http.StatusForbidden, "workbench review requires a user caller", "worker and job credentials may not review a thread")
		return
	}
	// Reviewing continues the thread with a review prompt (a new job over the agent session),
	// which is 对会话 job 说话: someone else's thread needs job.manage.others (design §1 /
	// plan T1.5c), the caller's own needs nothing.
	if owner, ok := s.workbenchThreadOwnerByID(c.Param("id")); ok && !s.mayManageOwnedRow(c, owner, false) {
		s.denyJobAction(c, authz.PermJobManageOthers)
		return
	}
	if s.workbench == nil {
		writeError(c, http.StatusServiceUnavailable, "workbench unavailable", "job metadata service is not wired")
		return
	}
	var body workbench.ReviewInput
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	result, err := s.workbench.Review(callerFromCtx(c), c.Param("id"), body)
	if err != nil {
		status := workbenchHTTPStatus(err)
		if errors.Is(err, workbench.ErrMissingDiffBase) {
			status = http.StatusConflict
		} else if errors.Is(err, workbench.ErrDiffTimeout) {
			status = http.StatusGatewayTimeout
		} else if errors.Is(err, workbench.ErrDiffUnavailable) {
			status = http.StatusInternalServerError
		}
		writeError(c, status, "review workbench thread failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}
