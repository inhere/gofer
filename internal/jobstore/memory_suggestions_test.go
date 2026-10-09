package jobstore

import (
	"encoding/json"
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

func TestMemorySuggestionTargetRevRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memsg-rev.db"))
	assert.NoErr(t, err)
	defer s.Close()
	m, _, err := s.AddMemorySuggestion(MemorySuggestion{TrackerID: "t", MemoryKey: "k", Action: "merge", BaseRev: 3, TargetRev: 7})
	assert.NoErr(t, err)
	got, err := s.GetMemorySuggestion(m.ID)
	assert.NoErr(t, err)
	assert.Eq(t, int64(3), got.BaseRev)
	assert.Eq(t, int64(7), got.TargetRev)
}

// TestTombstoneTrackerMemoryAt: the compare-and-set tombstone applies only to a live
// memory still at the expected rev.
func TestTombstoneTrackerMemoryAt(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tomb.db"))
	assert.NoErr(t, err)
	defer s.Close()
	assert.NoErr(t, s.UpsertTrackerMemory(TrackerRecord{TrackerID: "t", ID: "k", Body: []byte(`{"content":"x"}`), Rev: 2, UpdatedAt: "u"}))

	_, err = s.TombstoneTrackerMemoryAt("t", "missing", 1, nil, "now", "archive:me")
	assert.True(t, errors.Is(err, ErrTrackerConflict))
	_, err = s.TombstoneTrackerMemoryAt("t", "k", 1, nil, "now", "archive:me")
	assert.True(t, errors.Is(err, ErrTrackerConflict))
	rec, _, _ := s.GetTrackerMemory("t", "k")
	assert.False(t, rec.Deleted)
	assert.Eq(t, int64(2), rec.Rev)

	out, err := s.TombstoneTrackerMemoryAt("t", "k", 2, []byte(`{"archive_reason":"r"}`), "now", "archive:me")
	assert.NoErr(t, err)
	assert.Eq(t, int64(3), out.Rev)
	rec, _, _ = s.GetTrackerMemory("t", "k")
	assert.True(t, rec.Deleted)
	assert.Eq(t, int64(3), rec.Rev)
	assert.Eq(t, "archive:me", rec.DeletedBy)
	assert.Eq(t, `{"archive_reason":"r"}`, string(rec.Body))
	assert.True(t, rec.ChangedSeq > 0)

	// already a tombstone: no second write
	_, err = s.TombstoneTrackerMemoryAt("t", "k", 3, nil, "now", "archive:me")
	assert.True(t, errors.Is(err, ErrTrackerConflict))
}

func TestPatchTrackerMemoryStampsBy(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "patch.db"))
	assert.NoErr(t, err)
	defer s.Close()
	assert.NoErr(t, s.UpsertTrackerMemory(TrackerRecord{TrackerID: "t", ID: "k", Body: []byte(`{"key":"k","content":"x","by":"alice"}`), Rev: 1, UpdatedAt: "u"}))

	out, err := s.PatchTrackerMemory("t", "k", 1, map[string]json.RawMessage{"content": json.RawMessage(`"y"`)}, "now", "human:me")
	assert.NoErr(t, err)
	assert.Eq(t, int64(2), out.Rev)
	var m map[string]any
	assert.NoErr(t, json.Unmarshal(out.Body, &m))
	assert.Eq(t, "y", m["content"])
	assert.Eq(t, "human:me", m["by"]) // clones read `by`, not updated_by
	assert.Eq(t, "now", m["updated_at"])

	_, err = s.PatchTrackerMemory("t", "k", 1, map[string]json.RawMessage{"content": json.RawMessage(`"z"`)}, "now", "human:me")
	assert.True(t, errors.Is(err, ErrTrackerConflict))
	// The conflict return must roll its transaction back: a leaked tx pins a pooled
	// connection (and, on Windows, keeps the db file open past Close).
	assert.Eq(t, 0, s.db.Stats().InUse)
}
