package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestTodaySnoozeStore(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_760_000_000, 0)
	s.SetClock(func() time.Time { return now })

	_, err := s.UpsertTodaySnooze(TodaySnooze{CardKey: " "})
	assert.Err(t, err)

	row, err := s.UpsertTodaySnooze(TodaySnooze{CardKey: "work:w1", UntilAt: now.Unix() + 3600, ActivityAt: 100})
	assert.NoErr(t, err)
	assert.Eq(t, now.Unix(), row.CreatedAt)
	_, err = s.UpsertTodaySnooze(TodaySnooze{CardKey: "interaction:j1/i1", UntilJobID: "j9", ActivityAt: 5})
	assert.NoErr(t, err)

	got, ok, err := s.GetTodaySnooze("work:w1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(100), got.ActivityAt)
	assert.Eq(t, int64(0), got.WokeAt)

	// Wake once; a second wake keeps the first reason.
	now = now.Add(time.Minute)
	assert.NoErr(t, s.WakeTodaySnooze("work:w1", "activity"))
	assert.NoErr(t, s.WakeTodaySnooze("work:w1", "time"))
	got, _, _ = s.GetTodaySnooze("work:w1")
	assert.Eq(t, now.Unix(), got.WokeAt)
	assert.Eq(t, "activity", got.WokeReason)

	// A re-snooze clears the marker and replaces the rule.
	_, err = s.UpsertTodaySnooze(TodaySnooze{CardKey: "work:w1", UntilJobID: "j2", ActivityAt: 200})
	assert.NoErr(t, err)
	got, _, _ = s.GetTodaySnooze("work:w1")
	assert.Eq(t, int64(0), got.WokeAt)
	assert.Eq(t, "", got.WokeReason)
	assert.Eq(t, int64(0), got.UntilAt)
	assert.Eq(t, "j2", got.UntilJobID)

	rows, err := s.ListTodaySnoozes()
	assert.NoErr(t, err)
	assert.Len(t, rows, 2)
	assert.Eq(t, "interaction:j1/i1", rows[0].CardKey)

	n, err := s.DeleteTodaySnoozes("work:w1", "nope")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)
	n, err = s.DeleteTodaySnoozes()
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
	_, ok, _ = s.GetTodaySnooze("work:w1")
	assert.False(t, ok)
}
