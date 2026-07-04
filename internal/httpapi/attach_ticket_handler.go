package httpapi

import (
	"net/http"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
)

const attachTicketTTL = 30 * time.Second

func (s *Server) handleAttachTicket(c *rux.Context) {
	id := c.Param("id")
	caller := callerFromCtx(c)
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if !s.callerMayAttach(caller, res) {
		writeError(c, http.StatusForbidden, "attach not permitted for this caller", "caller lacks permission to attach to this job")
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
		Caller: caller,
		JobID:  id,
		Mode:   mode,
		Origin: c.Req.Header.Get("Origin"),
		Expiry: expiry,
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
