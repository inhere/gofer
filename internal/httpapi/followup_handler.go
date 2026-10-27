package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

type followUpReq struct {
	Prompt     string `json:"prompt"`
	ReviewEach bool   `json:"review_each,omitempty"`
}

func (s *Server) followUpAccess(c *rux.Context, id string) bool {
	if res, ok := s.jobForAccess(id); ok && !s.mayManageJob(c, res, true) {
		s.denyJobAction(c, authz.PermJobManageOthers, authz.PermJobSubmit)
		return false
	}
	return true
}

func (s *Server) handleQueueFollowUp(c *rux.Context) {
	id := c.Param("id")
	if !s.followUpAccess(c, id) {
		return
	}
	var req followUpReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	out, err := s.jobs.QueueFollowUp(id, req.Prompt, callerFromCtx(c), req.ReviewEach)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, job.ErrUnknownJob) {
			status = http.StatusNotFound
		}
		if errors.Is(err, job.ErrFollowUpNotLive) {
			status = http.StatusConflict
		}
		writeError(c, status, "follow-up rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) handleListFollowUps(c *rux.Context) {
	id := c.Param("id")
	if !s.followUpAccess(c, id) {
		return
	}
	out, err := s.jobs.ListFollowUps(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, job.ErrUnknownJob) {
			status = http.StatusNotFound
		}
		writeError(c, status, "list follow-ups failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"follow_ups": out})
}

func (s *Server) handleDropFollowUp(c *rux.Context) {
	id := c.Param("id")
	if !s.followUpAccess(c, id) {
		return
	}
	fid, err := strconv.ParseInt(c.Param("follow_up_id"), 10, 64)
	if err != nil || fid <= 0 {
		writeError(c, http.StatusBadRequest, "invalid follow-up id", "follow_up_id must be a positive integer")
		return
	}
	if err := s.jobs.DropFollowUp(id, fid); err != nil {
		status := http.StatusConflict
		if errors.Is(err, job.ErrUnknownJob) {
			status = http.StatusNotFound
		}
		writeError(c, status, "drop follow-up failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"dropped": fid})
}
