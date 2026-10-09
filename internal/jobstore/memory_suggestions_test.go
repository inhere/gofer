package jobstore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestMemorySuggestionsStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memsg.db"))
	assert.NoErr(t, err)
	defer s.Close()
	now := time.Unix(1_760_000_000, 0)
	s.SetClock(func() time.Time { return now })

	_, _, err = s.AddMemorySuggestion(MemorySuggestion{TrackerID: "t"})
	assert.True(t, errors.Is(err, ErrWorkInvalid))

	a, rec, err := s.AddMemorySuggestion(MemorySuggestion{TrackerID: "t", MemoryKey: "k", Action: "archive", Reason: "old", By: "steward(codex)"})
	assert.NoErr(t, err)
	assert.True(t, rec)
	assert.Eq(t, MemorySuggestPending, a.State)
	assert.Eq(t, "{}", a.PayloadJSON)
	dup, rec, err := s.AddMemorySuggestion(MemorySuggestion{TrackerID: "t", MemoryKey: "k", Action: "archive", Reason: "again"})
	assert.NoErr(t, err)
	assert.False(t, rec)
	assert.Eq(t, a.ID, dup.ID)
	b, _, err := s.AddMemorySuggestion(MemorySuggestion{TrackerID: "t", MemoryKey: "k", Action: "summary", PayloadJSON: `{"summary":"x"}`})
	assert.NoErr(t, err)

	n, err := s.CountMemorySuggestionsSince(now.Unix())
	assert.NoErr(t, err)
	assert.Eq(t, 2, n)

	d, err := s.DecideMemorySuggestion(a.ID, MemorySuggestDismissed, "human:me", "")
	assert.NoErr(t, err)
	assert.Eq(t, MemorySuggestDismissed, d.State)
	assert.Eq(t, now.Unix(), d.DecidedAt)
	_, err = s.DecideMemorySuggestion(a.ID, MemorySuggestAdopted, "human:me", "")
	assert.True(t, errors.Is(err, ErrMemorySuggestionDecided))
	_, err = s.DecideMemorySuggestion(999, MemorySuggestAdopted, "human:me", "")
	assert.True(t, errors.Is(err, ErrMemorySuggestionNotFound))
	_, err = s.DecideMemorySuggestion(b.ID, "bogus", "human:me", "")
	assert.Err(t, err)

	dis, err := s.MemorySuggestionDismissedSince("t", "k", "archive", now.Unix()-10)
	assert.NoErr(t, err)
	assert.True(t, dis)
	dis, err = s.MemorySuggestionDismissedSince("t", "k", "archive", now.Unix()+10)
	assert.NoErr(t, err)
	assert.False(t, dis)

	pending, err := s.ListMemorySuggestions(MemorySuggestPending)
	assert.NoErr(t, err)
	assert.Len(t, pending, 1)
	all, err := s.ListMemorySuggestions("")
	assert.NoErr(t, err)
	assert.Len(t, all, 2)

	_, ok, err := s.GetTrackerMemory("t", "k")
	assert.NoErr(t, err)
	assert.False(t, ok)
	assert.NoErr(t, s.UpsertTrackerMemory(TrackerRecord{TrackerID: "t", ID: "k", Body: []byte(`{"key":"k"}`), Rev: 1, UpdatedAt: "2026-01-01T00:00:00Z"}))
	got, ok, err := s.GetTrackerMemory("t", "k")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(1), got.Rev)
	assert.False(t, got.Deleted)
}
