package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestUpsertGetWrapUpRoundTrip (gofer-gk74): the agent's own 收尾原因 persists on the
// job row, survives a later upsert of the same row, and a job without one reads back ""
// (the column's COALESCE default) rather than looking wrapped up.
func TestUpsertGetWrapUpRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleJob("wrap-1", "alpha", 1000)
	in.WrapUp = "max_steps"
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("wrap-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "max_steps", got.WrapUp)

	// A re-upsert (the terminal write) overwrites it in place.
	in.WrapUp = "max_tokens"
	assert.NoErr(t, s.UpsertJob(in))
	got, _, _ = s.GetJob("wrap-1")
	assert.Eq(t, "max_tokens", got.WrapUp)

	none := sampleJob("wrap-none", "alpha", 1001)
	assert.NoErr(t, s.UpsertJob(none))
	plain, _, _ := s.GetJob("wrap-none")
	assert.Eq(t, "", plain.WrapUp)
}
