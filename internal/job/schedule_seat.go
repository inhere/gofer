package job

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// SeatDeliverRequest is one due run of a schedule whose target is a seat: the text goes to
// whatever session sits in the seat at that moment.
type SeatDeliverRequest struct {
	SeatID       string
	Text         string
	ScheduleID   string
	ScheduleName string
	// Origin is the ceiling owner of the delivery: the schedule's creator, because nobody
	// asked for this run and it must stay bounded by whoever configured the timer.
	Origin string
}

// SeatDeliverResult says how the delivery ended. Outcome is one of the
// jobstore.ScheduleResult* values; JobID is the restarted job of an ACP seat.
type SeatDeliverResult struct {
	Outcome string
	JobID   string
}

// SeatDeliverer hands a schedule's text to a seat. The seat package depends on job, so job
// only names this seam: the server wires the seat service in (see
// ScheduleDispatcher.SetSeatDeliverer).
type SeatDeliverer interface {
	DeliverToSeat(ctx context.Context, req SeatDeliverRequest) (SeatDeliverResult, error)
}

// ErrSeatDeliveryUnavailable: the schedule targets a seat but this server has no seat service.
var ErrSeatDeliveryUnavailable = errors.New("seat schedules are not available on this server")

// seatSlot holds the injected deliverer.
type seatSlot struct {
	mu sync.RWMutex
	d  SeatDeliverer
}

func (l *seatSlot) get() SeatDeliverer {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.d
}

// SetSeatDeliverer injects the seat delivery of seat-targeted schedules.
func (d *ScheduleDispatcher) SetSeatDeliverer(sd SeatDeliverer) {
	d.seats.mu.Lock()
	defer d.seats.mu.Unlock()
	d.seats.d = sd
}

// seatDeliveryTimeout bounds one delivery (a terminal message waits on the relay).
const seatDeliveryTimeout = 2 * time.Minute

// dispatchSeat is Dispatch for a schedule that delivers to a seat. The overlap rule and the
// max_runs guard have already run; the run and failure bookkeeping is the same one a job run
// uses: a delivery counts as a run, a refused one adds to the failure streak (and pauses the
// schedule at max_failures), a delivered one clears the streak. An offline seat is neither:
// the text could not be handed over, and a card asks the person to open the seat.
func (d *ScheduleDispatcher) dispatchSeat(rec jobstore.ScheduleRecord, trigger, seatID string, req JobRequest) (ScheduleRun, error) {
	sd := d.seats.get()
	if sd == nil {
		err := ErrSeatDeliveryUnavailable
		d.recordSeatResult(rec, jobstore.ScheduleResultFailed+":"+err.Error())
		d.submitFailed(rec, trigger, err)
		return ScheduleRun{}, err
	}
	origin := req.OriginCaller
	if origin == "" {
		origin = rec.CreatorCaller
	}
	ctx, cancel := context.WithTimeout(context.Background(), seatDeliveryTimeout)
	defer cancel()
	res, err := sd.DeliverToSeat(ctx, SeatDeliverRequest{
		SeatID: seatID, Text: req.Prompt, ScheduleID: rec.ID, ScheduleName: rec.Name, Origin: origin,
	})
	if err != nil {
		d.recordSeatResult(rec, jobstore.ScheduleResultFailed+":"+err.Error())
		d.submitFailed(rec, trigger, err)
		return ScheduleRun{}, err
	}
	result := res.Outcome
	if res.JobID != "" {
		result += ":" + res.JobID
	}
	d.recordSeatResult(rec, result)
	if scheduleCounts(rec, trigger) {
		d.runSubmitted(rec, res.JobID)
	}
	switch res.Outcome {
	case jobstore.ScheduleResultOffline:
		d.s.RecordScopedEvent(ScheduleEventScope(rec.ID), EventScheduleSeatPending, rec.ProjectKey,
			map[string]any{"schedule_id": rec.ID, "seat_id": seatID})
	default:
		if scheduleCounts(rec, trigger) {
			if rerr := d.s.meta.ResetScheduleFailures(rec.ID); rerr != nil {
				slog.Warn("schedule seat reset failures", "schedule_id", rec.ID, "err", rerr)
			}
		}
	}
	return ScheduleRun{Result: JobResult{ID: res.JobID}}, nil
}

// recordSeatResult stores how the last delivery ended; the schedule views show it.
func (d *ScheduleDispatcher) recordSeatResult(rec jobstore.ScheduleRecord, result string) {
	if err := d.s.meta.SetScheduleResult(rec.ID, result); err != nil {
		slog.Warn("schedule seat result", "schedule_id", rec.ID, "err", err)
	}
}
