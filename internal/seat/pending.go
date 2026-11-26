package seat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// The queue of seat-schedule texts that found nobody home (jobstore.SeatPending). A due run
// whose seat cannot take it is queued by DeliverToSeat and sent late by FlushPending, which
// the server calls when a session is bound into the seat (Bind) and on its periodic sweep.
// The late send goes through deliverNow, the very path a due run takes.

// FlushPending sends the queued texts of every seat that can take them now and drops the
// expired ones. It returns how many texts went out; a failed send stays queued for the next
// pass.
func (s *Service) FlushPending(ctx context.Context) (int, error) {
	if _, err := s.store.DropExpiredSeatPending(); err != nil {
		return 0, err
	}
	ids, err := s.store.SeatPendingSeatIDs()
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, id := range ids {
		n, err := s.flushSeat(ctx, id)
		sent += n
		if err != nil {
			slog.Warn("seat pending flush", "seat_id", id, "err", err)
		}
	}
	return sent, nil
}

// flushSeat sends the queued texts of one seat, if its session can take them.
func (s *Service) flushSeat(ctx context.Context, seatID string) (int, error) {
	x, err := s.Get(seatID)
	if err != nil || x.ArchivedAt != 0 {
		if err != nil && !errors.Is(err, jobstore.ErrSeatNotFound) {
			return 0, err
		}
		return 0, s.store.DropSeatPending(seatID)
	}
	rows, err := s.store.ListSeatPending(seatID)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, p := range rows {
		st, err := s.Status("", x)
		if err != nil {
			return sent, err
		}
		ok, err := s.store.ClaimSeatPending(p.ID)
		if err != nil || !ok {
			if err != nil {
				return sent, err
			}
			continue
		}
		req := job.SeatDeliverRequest{
			SeatID: seatID, Text: s.lateText(p), ScheduleID: p.ScheduleID, ScheduleName: p.ScheduleName, Origin: p.Origin,
		}
		res, err := s.deliverNow(ctx, x, st, s.scheduledText(req), req)
		if err != nil || res.Outcome == jobstore.ScheduleResultOffline {
			_ = s.store.ReleaseSeatPending(p.ID)
			if err != nil {
				return sent, err
			}
			// Nobody can take it: the rest of the seat's queue cannot either.
			return sent, nil
		}
		if err := s.store.FinishSeatPending(p); err != nil {
			return sent, err
		}
		result := res.Outcome
		if res.JobID != "" && res.Outcome == jobstore.ScheduleResultRestarted {
			result += ":" + res.JobID
		}
		if err := s.store.SetScheduleResult(p.ScheduleID, result); err != nil {
			slog.Warn("seat pending result", "schedule_id", p.ScheduleID, "err", err)
		}
		sent++
	}
	return sent, nil
}

// FlushSeatOf sends what is queued for the seat a session / job sits in, if it can take it now,
// and does nothing for a ref that sits in no seat or whose queue is empty. It is the immediate
// late send for a session that just came back (a registration / a beat after offline), so a
// person whose terminal was away does not wait for the periodic sweep.
//
// The send goes through flushSeat, which claims each row conditionally: two reports for the same
// session (a SessionStart followed by ticks, the sweep racing this one) never send a text twice,
// and a report for a seat whose session still cannot take it leaves the row queued.
func (s *Service) FlushSeatOf(ctx context.Context, ref string) (int, error) {
	if strings.TrimSpace(ref) == "" {
		return 0, nil
	}
	x, ok, err := s.SeatOfRef(ref)
	if err != nil || !ok {
		return 0, err
	}
	return s.flushSeat(ctx, x.ID)
}

// lateText is the queued text with the line that says it is late: when it was due and how
// many runs went undelivered.
func (s *Service) lateText(p jobstore.SeatPending) string {
	at := time.Unix(p.UpdatedAt, 0).Format("2006-01-02 15:04")
	note := i18n.T(s.lang(), "seat.schedule.late", "at", at, "n", fmt.Sprint(p.Count))
	return strings.TrimRight(note, "\n") + "\n" + p.Text
}

// bindFlushTimeout bounds the late send that follows a bind.
const bindFlushTimeout = 30 * time.Second

// flushAfterBind sends what was queued for the seat now that a session sits in it. A failure
// is only logged: the bind itself succeeded and the sweep tries again.
func (s *Service) flushAfterBind(seatID string) {
	rows, err := s.store.ListSeatPending(seatID)
	if err != nil || len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), bindFlushTimeout)
	defer cancel()
	if _, err := s.flushSeat(ctx, seatID); err != nil {
		slog.Warn("seat pending flush after bind", "seat_id", seatID, "err", err)
	}
}
