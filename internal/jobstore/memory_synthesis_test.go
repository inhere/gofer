package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestMemorySynthesisRunRoundTrip(t *testing.T) {
	s := openTest(t)
	defer s.Close()
	r := MemorySynthesisRun{ID: "run-1", ProjectKey: "proj", Day: "2026-10-28", Status: "succeeded", InputDigest: "digest", MaterialSchema: "daily-memory-materials.v1", StartedAt: 10, FinishedAt: 20, MaterialCountsJSON: `{"job":1}`, OmittedJSON: `{}`}
	assert.NoErr(t, s.InsertMemorySynthesisRun(r))
	got, ok, err := s.GetMemorySynthesisRun("run-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, r.ProjectKey, got.ProjectKey)
	assert.Eq(t, r.InputDigest, got.InputDigest)
	rows, err := s.ListMemorySynthesisRuns(MemorySynthesisRunFilter{ProjectKey: "proj"})
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
}
