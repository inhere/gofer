package httpapi

import (
	"context"
	"log/slog"
)

// sweepSeatPending is one pass of the late send of queued seat schedule texts: every seat
// whose session can take them now gets them, and the expired ones are dropped. It rides the
// session-nudge sweeper's tick; a failure is logged and the next pass retries.
func (s *Server) sweepSeatPending(ctx context.Context) {
	svc := s.seats()
	if svc == nil {
		return
	}
	if _, err := svc.FlushPending(ctx); err != nil {
		slog.Warn("seat pending sweep failed", "event", "seat.pending_sweep_failed", "err", err)
	}
}
