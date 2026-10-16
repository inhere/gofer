package httpapi

import (
	"net/http"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
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
	if !s.mayAttachJob(c, res) {
		s.denyJobAction(c, authz.PermJobAttachOthers)
		return
	}
	if !res.Interactive {
		writeError(c, http.StatusConflict, "job is not interactive", "attach tickets require an interactive job")
		return
	}
	if job.IsFinished(res.Status) {
		writeError(c, http.StatusConflict, "job is finished", "cannot attach to a job that has finished ("+res.Status+")")
		return
	}
	if s.ptyRelays == nil {
		writeError(c, http.StatusConflict, "relay not live", "job has no live pty relay")
		return
	}
	if !s.canAttachNow(c, res) {
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
		// Design §6: the ticket carries the caller and the dependency set (the handshake cannot
		// present a token), and the permission this connection will rest on — the attach pair
		// mayAttachJob just decided, so a later revocation can end the relay.
		Session: requestSessionID(c),
		Token:   presentedToken(c),
		Perms:   attachLiveConnPerms(c, res),
		Deps:    s.decisionOf(c).GrantIDs(),
	})
	c.JSON(http.StatusOK, map[string]any{
		"ticket":     ticket,
		"expires_in": int(attachTicketTTL / time.Second),
	})
}

// attachLiveConnPerms is what an attach relay rests on: the caller's own job needs job.attach (the
// attach gate every attach path asks), someone else's needs job.attach.others — and a caller that is
// not a user-class caller needs neither (design §2: permissions do not govern a job credential).
func attachLiveConnPerms(c *rux.Context, res job.JobResult) []string {
	if callerKindFromCtx(c) != callerKindUser {
		return nil
	}
	if res.CallerID != "" && res.CallerID == callerFromCtx(c) {
		return []string{authz.PermJobAttach}
	}
	return []string{authz.PermJobAttachOthers}
}

// canAttachNow reports whether a live pty relay exists for a job the caller may attach
// to (mayAttachJob is the permission rule: the caller's own job needs job.attach, someone
// else's or an ownerless one needs job.attach.others). It is the attachment availability
// bit of the job detail view and the last check of the attach-ticket route.
func (s *Server) canAttachNow(c *rux.Context, res job.JobResult) bool {
	if !res.Interactive || job.IsFinished(res.Status) || !s.mayAttachJob(c, res) {
		return false
	}
	if s.ptyRelays == nil {
		return false
	}
	entry, ok := s.ptyRelays.Lookup(res.ID)
	return ok && entry.Relay != nil &&
		(entry.State == ptyrelay.RelayOpen || entry.State == ptyrelay.RelayAttached)
}
