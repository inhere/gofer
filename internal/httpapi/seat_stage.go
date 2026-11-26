package httpapi

import (
	"strings"

	"github.com/inhere/gofer/internal/seat"
	"github.com/inhere/gofer/internal/stage"
	"github.com/inhere/gofer/internal/stagenote"
)

// The `stage` object of a seat row: where the flow of the work item the sitting session is on
// stands, so the roster can show the current step next to the state dot without a second request
// per row. It reads stored rows only (no gate, no git), like the session view's own stage.

// seatStageBriefOf is that object for the session `ref` sitting in a seat: nil when the seat is
// vacant, the session has no flow in progress, or this server has no stage service.
//
// The store has no flow query for a set of sessions (WorkStagesForSession takes one id), so a
// roster of n seats costs one ForSession per seat that has a session — the flow plus the project
// of its work item. Nothing here may fail the roster: a lookup error only leaves the field out.
func (s *Server) seatStageBriefOf(ref string) *stagenote.Brief {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	svc := s.stages()
	if svc == nil {
		return nil
	}
	v, ok, err := svc.ForSession(ref, false)
	if err != nil || !ok {
		return nil
	}
	b := stage.BriefOf(v)
	return &b
}

// seatView is seatViewOf plus what needs the server: the flow of the sitting session.
func (s *Server) seatView(st seat.Status) seatView {
	v := seatViewOf(st)
	v.Stage = s.seatStageBriefOf(st.Seat.CurrentRef)
	return v
}

// seatViews renders a list of seats for the roster routes.
func (s *Server) seatViews(sts []seat.Status) []seatView {
	out := make([]seatView, 0, len(sts))
	for _, st := range sts {
		out = append(out, s.seatView(st))
	}
	return out
}
