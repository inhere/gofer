package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/preview"
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
	c.JSON(http.StatusCreated, map[string]any{"id": lease.ID, "url": url, "port": lease.Port, "target_port": req.Port, "expires_at": lease.ExpiresAt, "worker_id": "local"})
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

// previewLeaseForChange resolves the job and lease a revoke / renew names and applies the
// design §3.6 rule: whoever can read the job may change a lease they created; changing
// someone else's lease needs the job's owner or job.manage.others. It answers the caller
// itself and returns false when it refused.
func (s *Server) previewLeaseForChange(c *rux.Context) (preview.Lease, bool) {
	if s.preview == nil {
		writeError(c, http.StatusNotFound, "preview disabled", "")
		return preview.Lease{}, false
	}
	res, ok := s.jobForAccess(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "job not found", "")
		return preview.Lease{}, false
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return preview.Lease{}, false
	}
	lease, found, err := s.preview.Get(c.Param("preview_id"))
	if err != nil || !found || lease.JobID != res.ID {
		writeError(c, http.StatusNotFound, "preview not found", "")
		return preview.Lease{}, false
	}
	if lease.CallerID != callerFromCtx(c) && !s.mayManageJob(c, res, false) {
		s.denyJobAction(c, authz.PermJobManageOthers)
		return preview.Lease{}, false
	}
	return lease, true
}

func (s *Server) handleCloseJobPreview(c *rux.Context) {
	lease, ok := s.previewLeaseForChange(c)
	if !ok {
		return
	}
	if err := s.preview.Revoke(lease.ID, callerFromCtx(c)); err != nil {
		writeError(c, http.StatusInternalServerError, "preview close failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"id": lease.ID, "state": "closed"})
}

type previewRenewRequest struct {
	TTLSec int `json:"ttl_sec"`
}

func (s *Server) handleRenewJobPreview(c *rux.Context) {
	lease, ok := s.previewLeaseForChange(c)
	if !ok {
		return
	}
	var req previewRenewRequest
	if c.Req.ContentLength != 0 {
		if err := c.BindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	renewed, err := s.preview.Renew(lease.ID, req.TTLSec, callerFromCtx(c))
	if errors.Is(err, preview.ErrNotActive) {
		writeError(c, http.StatusConflict, "preview is not active", "")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "preview renew failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"id": renewed.ID, "state": renewed.State, "expires_at": renewed.ExpiresAt, "port": renewed.Port})
}
