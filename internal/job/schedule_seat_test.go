package job

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

type fakeSeatDeliverer struct {
	calls []SeatDeliverRequest
	res   SeatDeliverResult
	err   error
}

func (f *fakeSeatDeliverer) DeliverToSeat(_ context.Context, r SeatDeliverRequest) (SeatDeliverResult, error) {
	f.calls = append(f.calls, r)
	return f.res, f.err
}

func seatSchedule(t *testing.T, s *Service, id string, mutate func(*jobstore.ScheduleRecord)) jobstore.ScheduleRecord {
	t.Helper()
	raw, err := json.Marshal(JobRequest{ProjectKey: "self", Prompt: "weekly report please"})
	if err != nil {
		t.Fatal(err)
	}
	rec := jobstore.ScheduleRecord{
		ID: id, Name: "weekly", ScheduleType: "cron", CronExpr: "* * * * *", RequestJSON: string(raw),
		Enabled: 1, NextRunAt: 100, ProjectKey: "self", Overlap: jobstore.ScheduleOverlapSkip, MaxFailures: 3,
		Target: jobstore.ScheduleTargetSeat("seat-1"), CreatorCaller: "u1", CreatedAt: 1, UpdatedAt: 1,
	}
	if mutate != nil {
		mutate(&rec)
	}
	if err := s.meta.InsertSchedule(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// A due seat schedule hands its text to the deliverer (as the schedule's creator) instead of
// submitting a job, and the outcome is stored on the schedule.
func TestSeatScheduleTimerDeliversTextInsteadOfSubmitting(t *testing.T) {
	s := newTestService(t, t.TempDir())
	sd := &fakeSeatDeliverer{res: SeatDeliverResult{Outcome: jobstore.ScheduleResultDelivered}}
	s.ScheduleDispatcher().SetSeatDeliverer(sd)
	rec := seatSchedule(t, s, "sch-seat", nil)
	if _, err := s.ScheduleDispatcher().FireTimer(nil, rec); err != nil {
		t.Fatal(err)
	}
	if len(sd.calls) != 1 || sd.calls[0].SeatID != "seat-1" || sd.calls[0].Text != "weekly report please" || sd.calls[0].Origin != "u1" {
		t.Fatalf("calls = %+v", sd.calls)
	}
	got := getSchedule(t, s, rec.ID)
	if got.LastResult != jobstore.ScheduleResultDelivered || got.RunCount != 1 {
		t.Fatalf("schedule = result %q runs %d", got.LastResult, got.RunCount)
	}
	if jobs, _ := s.meta.ListJobs(jobstore.ListQuery{}); len(jobs) != 0 {
		t.Fatalf("a seat schedule submits no job: %d", len(jobs))
	}
}

// An offline seat is not a failure: the run counts, the streak is untouched, the result and an
// event say what happened.
func TestSeatScheduleOfflineRecordsResultAndEvent(t *testing.T) {
	s := newTestService(t, t.TempDir())
	s.ScheduleDispatcher().SetSeatDeliverer(&fakeSeatDeliverer{res: SeatDeliverResult{Outcome: jobstore.ScheduleResultOffline}})
	rec := seatSchedule(t, s, "sch-off", nil)
	for range 5 {
		if _, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, rec.ID)); err != nil {
			t.Fatal(err)
		}
	}
	got := getSchedule(t, s, rec.ID)
	if got.LastResult != jobstore.ScheduleResultOffline || got.Enabled != 1 || got.ConsecutiveFailures != 0 {
		t.Fatalf("schedule = %+v", got)
	}
	if n := countType(scheduleEventTypes(t, s, rec.ID), EventScheduleSeatPending); n != 5 {
		t.Fatalf("seat_pending events = %d, want 5", n)
	}
}

// Refused deliveries use the ordinary failure streak: at max_failures the schedule is paused.
func TestSeatScheduleConsecutiveFailuresPause(t *testing.T) {
	s := newTestService(t, t.TempDir())
	s.ScheduleDispatcher().SetSeatDeliverer(&fakeSeatDeliverer{err: errors.New("seat is archived")})
	rec := seatSchedule(t, s, "sch-fail", nil)
	for range 3 {
		if _, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, rec.ID)); err == nil {
			t.Fatal("want the delivery error")
		}
	}
	got := getSchedule(t, s, rec.ID)
	if got.Enabled != 0 || got.PausedReason != jobstore.SchedulePausedFailures {
		t.Fatalf("schedule = enabled %d reason %q, want paused by failures", got.Enabled, got.PausedReason)
	}
	if got.LastResult == "" || got.LastResult[:6] != "failed" {
		t.Fatalf("last result = %q", got.LastResult)
	}
}

// A server without the seat service fails the run instead of dropping it silently.
func TestSeatScheduleWithoutDelivererFails(t *testing.T) {
	s := newTestService(t, t.TempDir())
	rec := seatSchedule(t, s, "sch-none", nil)
	if _, err := s.ScheduleDispatcher().FireTimer(nil, rec); !errors.Is(err, ErrSeatDeliveryUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
