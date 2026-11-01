package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

func seedMirrorMemories(t *testing.T, meta *jobstore.Store, keys ...string) {
	t.Helper()
	old := time.Now().Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	assert.NoErr(t, meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "trk", ProjectKey: "self"}))
	for _, key := range keys {
		body, _ := json.Marshal(map[string]any{"key": key, "content": "content of " + key, "kind": "note", "updated_at": old})
		assert.NoErr(t, meta.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: "trk", ID: key, Body: body, Rev: 1, UpdatedAt: old}))
	}
}

func TestMemoryFindingsEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	meta := s.jobs.Meta()
	s.SetTrackerStore(meta)
	seedMirrorMemories(t, meta, "old-note", "other")

	// The memory list carries the server-side doctor flags.
	var list struct {
		Memories []jobstore.TrackerRecord `json:"memories"`
		Doctor   map[string][]struct {
			Slug string `json:"slug"`
		} `json:"doctor"`
	}
	todayCall(t, s, http.MethodGet, "/v1/tracker/memories?tracker_id=trk", testToken, nil, 200, &list)
	assert.Len(t, list.Memories, 2)
	assert.Eq(t, "note-stale", list.Doctor["old-note"][0].Slug)

	var findings map[string]json.RawMessage
	steward := seedJobToken(t, s, "job-mem-steward", jobstore.JobCredentialSteward, "")
	todayCall(t, s, http.MethodGet, "/v1/memory-findings?tracker_id=trk", steward, nil, 200, &findings)
	var items []map[string]any
	assert.NoErr(t, json.Unmarshal(findings["findings"], &items))
	assert.Len(t, items, 2)
	_, hasActions := items[0]["actions"]
	assert.False(t, hasActions)
	_, hasCap := findings["daily_cap"]
	assert.False(t, hasCap)
}

// The model-written suggestion queue is gone: its routes no longer exist.
func TestMemorySuggestionRoutesAreGone(t *testing.T) {
	s := newTestServer(t, testToken, false)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/memory-suggestions"},
		{http.MethodPost, "/v1/memory-suggestions"},
		{http.MethodPost, "/v1/memory-suggestions/1/adopt"},
		{http.MethodPost, "/v1/memory-suggestions/1/dismiss"},
	} {
		resp := do(t, s, c.method, c.path, testToken, map[string]any{})
		resp.Body.Close()
		assert.True(t, resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed, c.method+" "+c.path)
	}
}

// 「待整理」 archive: an archive tombstone, so clones move their copy to the archive file.
func TestTrackerMemoryArchiveEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	meta := s.jobs.Meta()
	s.SetTrackerStore(meta)
	seedMirrorMemories(t, meta, "old-note", "other")
	url := "/v1/tracker/memories/old-note/archive?tracker_id=trk"

	// A job credential may not archive; a stale rev conflicts and changes nothing.
	steward := seedJobToken(t, s, "job-mem-steward", jobstore.JobCredentialSteward, "")
	todayCall(t, s, http.MethodPost, url, steward, map[string]any{}, 403, nil)
	todayCall(t, s, http.MethodPost, url, testToken, map[string]any{"expected_rev": 7}, 409, nil)
	rec, _, _ := meta.GetTrackerMemory("trk", "old-note")
	assert.False(t, rec.Deleted)
	todayCall(t, s, http.MethodPost, "/v1/tracker/memories/old-note/archive", testToken, map[string]any{}, 400, nil)

	todayCall(t, s, http.MethodPost, url, testToken, map[string]any{"reason": "settled", "expected_rev": 1}, 200, nil)
	rec, _, _ = meta.GetTrackerMemory("trk", "old-note")
	assert.True(t, rec.Deleted)
	assert.True(t, tracker.IsArchiveTombstone(rec.DeletedBy))
	assert.True(t, strings.Contains(string(rec.Body), "settled"))
	// Archiving again, or a key that is not there, is a conflict.
	todayCall(t, s, http.MethodPost, url, testToken, map[string]any{}, 409, nil)
	todayCall(t, s, http.MethodPost, "/v1/tracker/memories/nope/archive?tracker_id=trk", testToken, map[string]any{}, 409, nil)
	other, _, _ := meta.GetTrackerMemory("trk", "other")
	assert.False(t, other.Deleted)
}

// 「待整理」 keep: unflag clears the flags of a mirrored memory.
func TestTrackerMemoryUnflagEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	meta := s.jobs.Meta()
	s.SetTrackerStore(meta)
	now := time.Now().UTC().Format(time.RFC3339)
	assert.NoErr(t, meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "trk", ProjectKey: "self"}))
	body, _ := json.Marshal(map[string]any{"key": "rule-a", "content": "a rule", "kind": "rule", "updated_at": now,
		"flags": []map[string]string{{"reason": "no longer true", "at": now}}})
	assert.NoErr(t, meta.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: "trk", ID: "rule-a", Body: body, Rev: 1, UpdatedAt: now}))

	var list struct {
		Doctor map[string][]struct {
			Slug string `json:"slug"`
		} `json:"doctor"`
	}
	todayCall(t, s, http.MethodGet, "/v1/tracker/memories?tracker_id=trk", testToken, nil, 200, &list)
	assert.Eq(t, "flagged", list.Doctor["rule-a"][0].Slug)

	steward := seedJobToken(t, s, "job-mem-steward", jobstore.JobCredentialSteward, "")
	url := "/v1/tracker/memories/rule-a/flag?tracker_id=trk"
	todayCall(t, s, http.MethodDelete, url, steward, nil, 403, nil)
	todayCall(t, s, http.MethodDelete, url, testToken, nil, 200, nil)
	todayCall(t, s, http.MethodDelete, "/v1/tracker/memories/missing/flag?tracker_id=trk", testToken, nil, 404, nil)

	list.Doctor = nil
	todayCall(t, s, http.MethodGet, "/v1/tracker/memories?tracker_id=trk", testToken, nil, 200, &list)
	assert.Len(t, list.Doctor["rule-a"], 0)
	rec, _, _ := meta.GetTrackerMemory("trk", "rule-a")
	assert.Eq(t, int64(2), rec.Rev)
	assert.False(t, rec.Deleted)
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }
