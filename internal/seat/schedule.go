package seat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// ScheduleOperator is the operator label of a message a seat schedule sends.
const ScheduleOperator = "schedule"

// ErrSeatArchived: a schedule targets a seat that has been archived.
var ErrSeatArchived = errors.New("seat is archived")

// DeliverToSeat implements job.SeatDeliverer: it hands the text of a due schedule to the
// session that sits in the seat right now. The session is read from the seat at delivery time
// (never from the schedule), so a seat that was rebound keeps receiving its schedule.
//
//   - A terminal session that is online gets the message through the relay's SendMessage.
//   - An ACP seat whose job lives gets it as a say; one whose job ended is restarted and the
//     text is its opening turn (sent once, inside the restart).
//   - A vacant seat, or a terminal session that is offline or ended, takes nothing: gofer does
//     not start a terminal for a person. The outcome is jobstore.ScheduleResultOffline.
func (s *Service) DeliverToSeat(ctx context.Context, req job.SeatDeliverRequest) (job.SeatDeliverResult, error) {
	x, err := s.Get(req.SeatID)
	if err != nil {
		return job.SeatDeliverResult{}, err
	}
	if x.ArchivedAt != 0 {
		return job.SeatDeliverResult{}, fmt.Errorf("%w: %s", ErrSeatArchived, x.ID)
	}
	text := s.scheduledText(req)
	offline := job.SeatDeliverResult{Outcome: jobstore.ScheduleResultOffline}
	st, err := s.Status("", x)
	if err != nil {
		return job.SeatDeliverResult{}, err
	}
	if x.Kind == jobstore.SeatKindACP {
		return s.deliverACP(ctx, x, st, text, req)
	}
	switch st.State {
	case StateNone, StateOffline, StateEnded:
		return offline, nil
	}
	if s.d.Messenger == nil {
		return job.SeatDeliverResult{}, errors.New("seat: this server cannot message sessions")
	}
	if _, err := s.d.Messenger.SendMessage(ctx, x.CurrentRef, text, ScheduleOperator+":"+req.ScheduleID, req.Origin); err != nil {
		return job.SeatDeliverResult{}, err
	}
	return job.SeatDeliverResult{Outcome: jobstore.ScheduleResultDelivered}, nil
}

func (s *Service) deliverACP(ctx context.Context, x jobstore.Seat, st Status, text string, req job.SeatDeliverRequest) (job.SeatDeliverResult, error) {
	switch st.State {
	case StateNone:
		return job.SeatDeliverResult{Outcome: jobstore.ScheduleResultOffline}, nil
	case StateEnded:
		plan, err := s.restart(ctx, x.ID, text)
		if err != nil {
			return job.SeatDeliverResult{}, err
		}
		return job.SeatDeliverResult{Outcome: jobstore.ScheduleResultRestarted, JobID: plan.JobID}, nil
	}
	if s.d.Sayer == nil {
		return job.SeatDeliverResult{}, errors.New("seat: this server cannot say to ACP sessions")
	}
	if err := s.d.Sayer.SaySession(x.CurrentRef, text); err != nil {
		return job.SeatDeliverResult{}, err
	}
	return job.SeatDeliverResult{Outcome: jobstore.ScheduleResultDelivered, JobID: x.CurrentRef}, nil
}

// scheduledText is the schedule's text with a system prefix that says where it came from.
func (s *Service) scheduledText(req job.SeatDeliverRequest) string {
	name := strings.TrimSpace(req.ScheduleName)
	if name == "" {
		name = req.ScheduleID
	}
	return i18n.T(s.lang(), "seat.schedule.prefix", "name", name) + "\n" + req.Text
}
