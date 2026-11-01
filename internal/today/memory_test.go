package today

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

func putMemory(t *testing.T, st *jobstore.Store, trackerID string, m map[string]any) {
	t.Helper()
	body, err := json.Marshal(m)
	assert.NoErr(t, err)
	key := m["key"].(string)
	updated, _ := m["updated_at"].(string)
	rev := int64(1)
	if rec, ok, _ := st.GetTrackerMemory(trackerID, key); ok {
		rev = rec.Rev + 1
	}
	assert.NoErr(t, st.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: trackerID, ID: key, Body: body, Rev: rev, UpdatedAt: updated}))
}

func daysAgo(d int) string {
	return testNow.Add(-time.Duration(d) * 24 * time.Hour).UTC().Format(time.RFC3339)
}

func seedMemories(t *testing.T, st *jobstore.Store) {
	t.Helper()
	assert.NoErr(t, st.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "trk", ProjectKey: "p1"}))
	putMemory(t, st, "trk", map[string]any{"key": "old-note", "content": "an old observation", "kind": "note", "updated_at": daysAgo(120)})
	putMemory(t, st, "trk", map[string]any{"key": "fresh", "content": "a fresh fact", "kind": "note", "updated_at": daysAgo(1)})
	putMemory(t, st, "trk", map[string]any{"key": "release-a", "content": "release steps: tag build push upgrade server", "kind": "note", "updated_at": daysAgo(2)})
	putMemory(t, st, "trk", map[string]any{"key": "release-b", "content": "release steps: tag build push upgrade server worker", "kind": "note", "updated_at": daysAgo(2)})
}

func memoryBody(t *testing.T, st *jobstore.Store, key string) (jobstore.TrackerRecord, tracker.Memory) {
	t.Helper()
	rec, ok, err := st.GetTrackerMemory("trk", key)
	assert.NoErr(t, err)
	assert.True(t, ok)
	var m tracker.Memory
	_ = json.Unmarshal(rec.Body, &m)
	return rec, m
}

func TestMemoryFindingsServerSide(t *testing.T) {
	svc, st := newTestService(t)
	seedMemories(t, st)

	got, err := svc.MemoryFindings(MemoryFindingsQuery{})
	assert.NoErr(t, err)
	byKey := map[string]MemoryFinding{}
	for _, f := range got {
		byKey[f.Key] = f
	}
	assert.Len(t, got, 3)
	assert.Eq(t, "p1", byKey["old-note"].ProjectKey)
	assert.Eq(t, tracker.DoctorNoteStale, byKey["old-note"].Findings[0].Slug)
	assert.Eq(t, tracker.DoctorDuplicate, byKey["release-a"].Findings[0].Slug)
	_, ok := byKey["fresh"]
	assert.False(t, ok)

	one, err := svc.MemoryFindings(MemoryFindingsQuery{TrackerID: "other"})
	assert.NoErr(t, err)
	assert.Len(t, one, 0)
}
