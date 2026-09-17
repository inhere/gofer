package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestJobReadOnlyRoundTrip: the read-only flag (bd h-aii-0ql3) is a persisted job
// property — a row written with it reads back set, and a row that never set it reads
// back false (the column's COALESCE default), so `job show` and the web console can
// state whether a FINISHED job ran read-only, not only the live request.
func TestJobReadOnlyRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleJob("ro-1", "alpha", 2000)
	in.ReadOnly = true
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("ro-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, got.ReadOnly)

	plain := sampleJob("ro-plain", "alpha", 2001)
	assert.NoErr(t, s.UpsertJob(plain))
	got2, _, err := s.GetJob("ro-plain")
	assert.NoErr(t, err)
	assert.False(t, got2.ReadOnly)
}
