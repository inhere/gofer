package httpapi

import (
	"net"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

type previewCreateRequest struct {
	Port   int `json:"port"`
	TTLSec int `json:"ttl_sec"`
}

func (s *Server) handleCreateJobPreview(c *rux.Context) {
	if s.preview == nil {
		writeError(c, http.StatusNotFound, "preview disabled", "")
		return
	}
	res, ok := s.jobForAccess(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "job not found", "")
		return
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	if !previewRunning(res) {
		writeError(c, http.StatusConflict, "job is not running", "")
		return
	}
	var req previewCreateRequest
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	host := c.Req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	lease, url, err := s.preview.Create(c.Req.Context(), res.ID, callerFromCtx(c), host, req.Port, req.TTLSec, true)
	if err != nil {
		writeError(c, http.StatusConflict, "preview unavailable", err.Error())
		return
	}
	c.JSON(http.StatusCreated, map[string]any{"id": lease.ID, "url": url, "port": req.Port, "expires_at": lease.ExpiresAt, "worker_id": "local"})
}
func previewRunning(r job.JobResult) bool {
	switch strings.ToLower(r.Status) {
	case "queued", "running", "awaiting_input", "waiting_input", "recovering":
		return true
	default:
		return false
	}
}
func (s *Server) handleListJobPreviews(c *rux.Context) {
	if s.preview == nil {
		writeError(c, http.StatusNotFound, "preview disabled", "")
		return
	}
	res, ok := s.jobForAccess(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "job not found", "")
		return
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	v, e := s.preview.List(res.ID)
	if e != nil {
		writeError(c, http.StatusInternalServerError, "preview list failed", e.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"previews": v})
}
func (s *Server) handleCloseJobPreview(c *rux.Context) {
	if s.preview == nil {
		writeError(c, http.StatusNotFound, "preview disabled", "")
		return
	}
	res, ok := s.jobForAccess(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "job not found", "")
		return
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	previewLease, found, err := s.preview.Get(c.Param("preview_id"))
	if err != nil || !found || previewLease.JobID != res.ID {
		writeError(c, http.StatusNotFound, "preview not found", "")
		return
	}
	if err := s.preview.Close(c.Param("preview_id"), "revoked"); err != nil {
		writeError(c, http.StatusInternalServerError, "preview close failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"id": c.Param("preview_id"), "state": "closed"})
}
