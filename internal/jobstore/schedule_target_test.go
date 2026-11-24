package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestSeatOfScheduleTarget(t *testing.T) {
	id, ok := SeatOfScheduleTarget("seat:abc")
	assert.True(t, ok)
	assert.Eq(t, "abc", id)
	for _, bad := range []string{"", "seat:", "seat:  ", "job:abc"} {
		_, ok := SeatOfScheduleTarget(bad)
		assert.False(t, ok, bad)
	}
	assert.Eq(t, "seat:s1", ScheduleTargetSeat("s1"))
}

func TestScheduleTargetAndResultRoundTrip(t *testing.T) {
	s := openTest(t)
	insertHealthSchedule(t, s, "sch-job", nil)
	insertHealthSchedule(t, s, "sch-seat", func(r *ScheduleRecord) { r.Target = ScheduleTargetSeat("seat-1") })

	got, ok, err := s.GetSchedule("sch-job")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", got.Target)

	assert.NoErr(t, s.SetScheduleResult("sch-seat", ScheduleResultOffline))
	got, _, err = s.GetSchedule("sch-seat")
	assert.NoErr(t, err)
	assert.Eq(t, "seat:seat-1", got.Target)
	assert.Eq(t, ScheduleResultOffline, got.LastResult)

	list, err := s.ListSeatSchedules("seat-1")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(list))
	assert.Eq(t, "sch-seat", list[0].ID)
}

// Archiving or deleting a seat stops the schedules that deliver to it and says why; the rows
// are kept, and schedules of other seats and plain job schedules are untouched.
func TestSeatArchiveAndDeleteStopItsSchedules(t *testing.T) {
	s := openTest(t)
	a := mustSeat(t, s, "u1", "proj", "a")
	b := mustSeat(t, s, "u1", "proj", "b")
	insertHealthSchedule(t, s, "sch-a", func(r *ScheduleRecord) { r.Target = ScheduleTargetSeat(a.ID) })
	insertHealthSchedule(t, s, "sch-b", func(r *ScheduleRecord) { r.Target = ScheduleTargetSeat(b.ID) })
	insertHealthSchedule(t, s, "sch-job", nil)

	_, err := s.ArchiveSeat(a.ID, "user")
	assert.NoErr(t, err)
	got, _, _ := s.GetSchedule("sch-a")
	assert.Eq(t, 0, got.Enabled)
	assert.Eq(t, SchedulePausedSeatArchived, got.PausedReason)
	for _, id := range []string{"sch-b", "sch-job"} {
		o, _, _ := s.GetSchedule(id)
		assert.Eq(t, 1, o.Enabled, id)
	}

	ok, err := s.DeleteSeat(b.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, found, _ := s.GetSchedule("sch-b")
	assert.True(t, found)
	assert.Eq(t, 0, got.Enabled)
	assert.Eq(t, SchedulePausedSeatDeleted, got.PausedReason)
}
