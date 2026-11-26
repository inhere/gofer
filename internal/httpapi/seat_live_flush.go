package httpapi

import (
	"context"
	"log/slog"
)

// flushSeatOnSessionLive is the sessionrelay came-back hook (sessionrelay.SetOnSessionLive):
// a terminal that was offline or ended is taking messages again, so whatever a seat schedule
// queued for it goes out now instead of at the next 30-second sweep. The relay knows nothing
// about seats (G021–G024); this is where the two meet.
//
// The report is already off the hook's request path (the relay runs it in its own goroutine
// under its own timeout), so this only has to be cheap and forgiving: a session in no seat, an
// empty queue and a seat whose session still cannot take the text are all no-ops, and a failure
// is logged — the queued text stays put and the sweep retries.
func (s *Server) flushSeatOnSessionLive(ctx context.Context, sid string) {
	svc := s.seats()
	if svc == nil {
		return
	}
	n, err := svc.FlushSeatOf(ctx, sid)
	if err != nil {
		slog.Warn("seat pending flush after the session came back", "session", sid, "err", err)
		return
	}
	if n > 0 {
		slog.Info("seat pending flush after the session came back", "event", "seat.pending_live_flush", "session", sid, "sent", n)
	}
}
