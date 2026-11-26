package httpapi

import (
	"context"
	"errors"

	"github.com/inhere/gofer/internal/seat"
	"github.com/inhere/gofer/internal/stage"
	"github.com/inhere/gofer/internal/work"
)

// deliverStageRemind is stage.DeliverFunc over the delivery paths this server already has:
//
//   - a session that sits on a seat goes through the seat service, the path a due seat
//     schedule takes (relay for a terminal, `job say` for an ACP job), and when the seat cannot
//     take it now the text waits in the seat's own pending queue (seat_pending_messages);
//   - any other session goes through work.AskSession (relay turn / messenger for a terminal,
//     `job say` for an ACP job). Those have no queue: an offline one is reported as not
//     delivered and the flow's journal says so — the per-turn stage line shows the step when
//     the session next speaks, so nothing is lost for it.
func (s *Server) deliverStageRemind(ctx context.Context, d stage.Delivery) (string, error) {
	if seats := s.seats(); seats != nil {
		out, handled, err := seats.DeliverStageNote(ctx, d.WorkItemID, d.SessionID, d.Step, d.Text)
		if err != nil {
			return "", err
		}
		if handled {
			switch out {
			case seat.StageNoteDelivered:
				return stage.DeliveredNow, nil
			case seat.StageNoteQueued:
				return stage.DeliveredQueued, nil
			}
			return stage.NotDelivered, nil
		}
	}
	if s.work == nil {
		return stage.NotDelivered, errors.New("work service unavailable")
	}
	_, err := s.work.AskSession(ctx, work.AskInput{
		SessionID: d.SessionID, Text: d.Text, By: "system", WorkID: d.WorkItemID,
		Allow: func(string, bool) bool { return true },
	})
	switch {
	case err == nil:
		return stage.DeliveredNow, nil
	case errors.Is(err, work.ErrAskOffline), errors.Is(err, work.ErrAskNotFound):
		return stage.NotDelivered, nil
	}
	return "", err
}
