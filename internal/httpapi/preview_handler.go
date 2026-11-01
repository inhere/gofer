package httpapi

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/preview"
	"github.com/inhere/gofer/internal/wsproto"
)

type previewCreateRequest struct {
	Port   int `json:"port"`
	TTLSec int `json:"ttl_sec"`
}

func (s *Server) handleCreateJobPreview(c *rux.Context) {
	if s.preview == nil {
		s.writeErrorKey(c, http.StatusNotFound, "preview disabled", "preview_disabled")
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
		s.writeErrorKey(c, http.StatusConflict, "job is not running", "preview_job_not_running")
		return
	}
	local := previewLocal(res)
	// Anything that is neither this server nor a worker is refused by name rather than
	// dialling this server's loopback for a job that runs somewhere else.
	if !local && res.WorkerID == "" {
		s.writeErrorKey(c, http.StatusConflict, "preview runner unsupported", "preview_runner_unsupported", "runner", previewRunnerLabel(res))
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
	var (
		lease preview.Lease
		url   string
		err   error
	)
	if local {
		lease, url, err = s.preview.Create(c.Req.Context(), res.ID, callerFromCtx(c), host, req.Port, req.TTLSec, true)
	} else {
		lease, url, err = s.preview.CreateOnWorker(c.Req.Context(), res.WorkerID, res.ID, callerFromCtx(c), host, req.Port, req.TTLSec, true)
	}
	switch {
	case errors.Is(err, preview.ErrWorkerOutdated):
		s.writeErrorKey(c, http.StatusConflict, "preview worker outdated", "preview_worker_outdated", "worker", res.WorkerID, "version", wsproto.PreviewMinProtocolVersion)
		return
	case errors.Is(err, preview.ErrWorkerOffline):
		s.writeErrorKey(c, http.StatusConflict, "preview worker offline", "preview_worker_offline", "worker", res.WorkerID)
		return
	case errors.Is(err, preview.ErrDisabled):
		s.writeErrorKey(c, http.StatusNotFound, "preview disabled", "preview_disabled")
		return
	case errors.Is(err, preview.ErrBadPort):
		s.writeErrorKey(c, http.StatusConflict, "preview unavailable", "preview_port_invalid")
		return
	case errors.Is(err, preview.ErrPoolExhausted):
		s.writeErrorKey(c, http.StatusConflict, "preview unavailable", "preview_pool_exhausted")
		return
	case err != nil:
		writeError(c, http.StatusConflict, "preview unavailable", err.Error())
		return
	}
	c.JSON(http.StatusCreated, map[string]any{"id": lease.ID, "url": url, "port": lease.Port, "target_port": req.Port, "expires_at": lease.ExpiresAt, "worker_id": lease.WorkerID})
}

// previewLocal reports whether the job runs on this server (the only place the lease
// proxy can reach): the built-in local runner and no worker recorded.
func previewLocal(r job.JobResult) bool {
	return r.WorkerID == "" && config.IsLocalRunnerName(r.Runner)
}

// previewRunnerLabel names the job's runner for the refusal text.
func previewRunnerLabel(r job.JobResult) string {
	if r.WorkerID != "" {
		return r.WorkerID
	}
	return r.Runner
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
		s.writeErrorKey(c, http.StatusNotFound, "preview disabled", "preview_disabled")
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
		s.writeErrorKey(c, http.StatusNotFound, "preview disabled", "preview_disabled")
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
		s.writeErrorKey(c, http.StatusConflict, "preview is not active", "preview_not_active")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "preview renew failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"id": renewed.ID, "state": renewed.State, "expires_at": renewed.ExpiresAt, "port": renewed.Port})
}

// handleWorkerPreviewConnect is the worker's callback for one preview data connection. It
// authenticates the worker itself (outside the /v1 caller group, like the tunnel callback)
// and hands the WebSocket to the preview manager, which owns the nonce / lease / instance
// checks and the relay.
func (s *Server) handleWorkerPreviewConnect(c *rux.Context) {
	ce, ok := s.tunnelAuth(c.Req)
	if !ok || ce.kind != callerKindWorker {
		slog.Warn("preview.rejected", "event", "preview.rejected", "component", "server", "reason", "unauthorized")
		http.Error(c.Resp, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(c.Resp, c.Req, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	s.preview.AcceptWorker(c.Req.Context(), ce.id, conn)
}

// NotePreviewPresence forwards worker presence changes to the preview manager, which closes
// the leases bound to a worker process that went away.
func (s *Server) NotePreviewPresence(workerID string, online bool) {
	if s.preview != nil {
		s.preview.NotePresence(workerID, online)
	}
}
