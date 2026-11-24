package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// seatScheduleTarget validates the delivery target of a schedule write and, for a seat
// target, completes req from the seat. isSeat is false for an empty target (a job schedule).
// ok is false after an error answer has been written.
//
// A seat target must name a seat that exists and is not archived (400), and the caller must be
// allowed to manage that seat — the same owner rule as the seat routes (maySeat: the owner, or
// seat.manage.others), else 403. The schedule's text is request.prompt; request.project_key is
// the seat's project and is filled in when omitted (a different one is refused).
func (s *Server) seatScheduleTarget(c *rux.Context, target string, req *job.JobRequest) (isSeat, ok bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return false, true
	}
	seatID, found := jobstore.SeatOfScheduleTarget(target)
	if !found {
		writeError(c, http.StatusBadRequest, "invalid schedule", `target must be empty or "seat:<seat id>"`)
		return false, false
	}
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "seats require a user caller", "worker and job credentials cannot use the seat roster")
		return false, false
	}
	svc := s.seats()
	if svc == nil {
		writeError(c, http.StatusServiceUnavailable, "seats unavailable", "no job store wired on this server")
		return false, false
	}
	x, err := svc.Get(seatID)
	if errors.Is(err, jobstore.ErrSeatNotFound) {
		writeError(c, http.StatusBadRequest, "invalid schedule", "target seat "+seatID+" does not exist")
		return false, false
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "seat lookup failed", err.Error())
		return false, false
	}
	if x.ArchivedAt != 0 {
		writeError(c, http.StatusBadRequest, "invalid schedule", "target seat "+seatID+" is archived")
		return false, false
	}
	if !s.maySeat(c, x.CallerID) {
		s.writePermissionDenied(c, authz.PermSeatManageOthers)
		return false, false
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(c, http.StatusBadRequest, "invalid schedule", "a seat schedule needs its text in request.prompt")
		return false, false
	}
	switch {
	case req.ProjectKey == "":
		req.ProjectKey = x.ProjectKey
	case req.ProjectKey != x.ProjectKey:
		writeError(c, http.StatusBadRequest, "invalid schedule", "request.project_key must be the seat's project "+x.ProjectKey)
		return false, false
	}
	return true, true
}

// writeSeatScheduleRun answers a run-now / webhook of a seat schedule: it submitted no job, so
// the answer is the schedule and how the delivery ended. It returns false for a job schedule.
func (s *Server) writeSeatScheduleRun(c *rux.Context, rec jobstore.ScheduleRecord) bool {
	if _, ok := jobstore.SeatOfScheduleTarget(rec.Target); !ok {
		return false
	}
	result := ""
	if cur, found, err := s.jobs.Meta().GetSchedule(rec.ID); err == nil && found {
		result = cur.LastResult
	}
	c.JSON(http.StatusOK, map[string]any{"schedule_id": rec.ID, "target": rec.Target, "result": result})
	return true
}
