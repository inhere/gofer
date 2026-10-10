package jobstore

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/tracker"
)

func TestFlagScopedMemoryKeepsUpdatedAtAndContentClears(t *testing.T) {
	s := openTest(t)
	put, err := s.PutScopedMemory(ScopedMemoryProject, "proj", "r", "body", nil, "alice")
	assert.Require(t, assert.NoErr(t, err))
	flag, _ := tracker.NewMemoryFlag("stale", "job-1", "job-1", time.Now())
	got, err := s.FlagScopedMemory(ScopedMemoryProject, "proj", "r", &flag)
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, got.Flags, 1)
	row, err := s.GetScopedMemory(ScopedMemoryProject, "proj", "r")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "stale", row.Flags[0].Reason)
	assert.Eq(t, put.UpdatedAt, row.UpdatedAt)

	// Same content keeps, new content clears (the review).
	_, err = s.PutScopedMemory(ScopedMemoryProject, "proj", "r", "body", nil, "alice")
	assert.Require(t, assert.NoErr(t, err))
	row, _ = s.GetScopedMemory(ScopedMemoryProject, "proj", "r")
	assert.Len(t, row.Flags, 1)
	_, err = s.PutScopedMemory(ScopedMemoryProject, "proj", "r", "new body", nil, "alice")
	assert.Require(t, assert.NoErr(t, err))
	row, _ = s.GetScopedMemory(ScopedMemoryProject, "proj", "r")
	assert.Empty(t, row.Flags)

	_, _ = s.FlagScopedMemory(ScopedMemoryProject, "proj", "r", &flag)
	got, err = s.FlagScopedMemory(ScopedMemoryProject, "proj", "r", nil)
	assert.Require(t, assert.NoErr(t, err))
	assert.Empty(t, got.Flags)

	_, err = s.FlagScopedMemory(ScopedMemoryProject, "proj", "missing", &flag)
	assert.True(t, errors.Is(err, ErrScopedMemoryNotFound))
}

func TestPatchTrackerMemoryContentClearsFlags(t *testing.T) {
	s := openTest(t)
	assert.Require(t, assert.NoErr(t, s.UpsertTrackerRepo(TrackerRepo{TrackerID: "t", Prefix: "p"})))
	body := `{"key":"m","content":"old","flags":[{"at":"2026-10-09T00:00:00Z","reason":"stale"}]}`
	assert.Require(t, assert.NoErr(t, s.UpsertTrackerMemory(TrackerRecord{TrackerID: "t", ID: "m", Body: json.RawMessage(body), Rev: 1})))
	rec, err := s.PatchTrackerMemory("t", "m", 0, map[string]json.RawMessage{"summary": json.RawMessage(`"s"`)}, "2026-10-09T01:00:00Z", "alice")
	assert.Require(t, assert.NoErr(t, err))
	assert.Contains(t, string(rec.Body), `"flags"`)
	rec, err = s.PatchTrackerMemory("t", "m", 0, map[string]json.RawMessage{"content": json.RawMessage(`"new"`)}, "2026-10-09T02:00:00Z", "alice")
	assert.Require(t, assert.NoErr(t, err))
	assert.NotContains(t, string(rec.Body), `"flags"`)
}
