package httpapi

import (
	"net/http"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/ptyrelay"
)

const attachTicketTTL = 30 * time.Second

func (s *Server) handleAttachTicket(c *rux.Context) {
	id := c.Param("id")
	caller := callerFromCtx(c)
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "attach not permitted for this caller", "worker tokens cannot request browser attach tickets")
		return
	}
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if !s.callerMayAttach(caller, res) {
		writeError(c, http.StatusForbidden, "attach not permitted for this caller", "caller lacks permission to attach to this job")
		return
	}
	if !res.Interactive {
		writeError(c, http.StatusConflict, "job is not interactive", "attach tickets require an interactive job")
		return
	}
	if job.IsTerminal(res.Status) {
		writeError(c, http.StatusConflict, "job is terminal", "cannot attach to a terminal job")
		return
	}
	if s.ptyRelays == nil {
		writeError(c, http.StatusConflict, "relay not live", "job has no live pty relay")
		return
	}
	if !s.canAttachNow(caller, res) {
		writeError(c, http.StatusConflict, "relay not live", "job has no open pty relay")
		return
	}
	entry, ok := s.ptyRelays.Lookup(id)
	if !ok {
		writeError(c, http.StatusConflict, "relay not live", "job has no open pty relay")
		return
	}

	mode := c.Query("mode")
	if mode == "" {
		mode = "write"
	}
	if mode != "write" && mode != "read" {
		writeError(c, http.StatusBadRequest, "invalid attach mode", "mode must be write or read")
		return
	}

	if s.attachTickets == nil {
		s.attachTickets = NewAttachTicketStore()
	}
	expiry := time.Now().Add(attachTicketTTL).Unix()
	ticket := s.attachTickets.Issue(AttachTicketBinding{
		Caller:       caller,
		JobID:        id,
		PtySessionID: entry.Binding.PtySessionID,
		Mode:         mode,
		Origin:       c.Req.Header.Get("Origin"),
		Expiry:       expiry,
	})
	c.JSON(http.StatusOK, map[string]any{
		"ticket":     ticket,
		"expires_in": int(attachTicketTTL / time.Second),
	})
}

func (s *Server) callerMayAttach(caller string, job job.JobResult) bool {
	if s.cfg != nil && s.cfg.Governance.RequireAttachCapability &&
		!s.cfg.CallerCanAttach(caller) && !s.cfg.CallerCanAdmin(caller) {
		return false
	}
	if job.CallerID == "" {
		return s.cfg != nil && s.cfg.CallerCanAdmin(caller)
	}
	return job.CallerID == caller || (s.cfg != nil && s.cfg.CallerCanAdmin(caller))
}

func (s *Server) canAttachNow(caller string, res job.JobResult) bool {
	if !res.Interactive || job.IsTerminal(res.Status) || !s.callerMayAttach(caller, res) {
		return false
	}
	if s.ptyRelays == nil {
		return false
	}
	entry, ok := s.ptyRelays.Lookup(res.ID)
	return ok && entry.Relay != nil &&
		(entry.State == ptyrelay.RelayOpen || entry.State == ptyrelay.RelayAttached)
}
