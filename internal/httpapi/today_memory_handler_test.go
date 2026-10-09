package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestMemorySuggestionEndpoints(t *testing.T) {
	s := newTestServer(t, testToken, false)
	meta := s.jobs.Meta()
	s.SetTrackerStore(meta)
	old := time.Now().Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	assert.NoErr(t, meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "trk", ProjectKey: "self"}))
	for _, key := range []string{"old-note", "other"} {
		body, _ := json.Marshal(map[string]any{"key": key, "content": "content of " + key, "kind": "note", "updated_at": old})
		assert.NoErr(t, meta.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: "trk", ID: key, Body: body, Rev: 1, UpdatedAt: old}))
	}

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

	var findings struct {
		Findings []struct {
			Key     string   `json:"key"`
			Actions []string `json:"actions"`
		} `json:"findings"`
		DailyCap int `json:"daily_cap"`
	}
	steward := seedJobToken(t, s, "job-mem-steward", jobstore.JobCredentialSteward, "")
	todayCall(t, s, http.MethodGet, "/v1/memory-findings?tracker_id=trk", steward, nil, 200, &findings)
	assert.Len(t, findings.Findings, 2)
	assert.Eq(t, 5, findings.DailyCap)

	body := map[string]any{"tracker_id": "trk", "key": "old-note", "action": "archive", "reason": "200 天未更新"}
	member := seedJobToken(t, s, "job-mem-member", jobstore.JobCredentialMember, "")
	todayCall(t, s, http.MethodPost, "/v1/memory-suggestions", member, body, 403, nil)
	var created struct {
		Suggestion struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		} `json:"suggestion"`
		Recorded bool `json:"recorded"`
	}
	todayCall(t, s, http.MethodPost, "/v1/memory-suggestions", steward, body, 201, &created)
	assert.True(t, created.Recorded)
	todayCall(t, s, http.MethodPost, "/v1/memory-suggestions", steward, body, 200, nil)
	todayCall(t, s, http.MethodPost, "/v1/memory-suggestions", steward, map[string]any{"tracker_id": "trk", "key": "nope", "action": "archive", "reason": "x"}, 404, nil)
	todayCall(t, s, http.MethodPost, "/v1/memory-suggestions", steward, map[string]any{"tracker_id": "trk", "key": "other", "action": "kind", "reason": "x"}, 400, nil)

	id := "/v1/memory-suggestions/" + jsonNum(created.Suggestion.ID)
	// Adopting / dismissing is a person's.
	todayCall(t, s, http.MethodPost, id+"/adopt", steward, nil, 403, nil)
	todayCall(t, s, http.MethodPost, id+"/adopt", testToken, nil, 200, nil)
	todayCall(t, s, http.MethodPost, id+"/adopt", testToken, nil, 409, nil)
	rec, _, _ := meta.GetTrackerMemory("trk", "old-note")
	assert.True(t, rec.Deleted)

	var sg struct {
		Suggestion struct {
			ID int64 `json:"id"`
		} `json:"suggestion"`
	}
	todayCall(t, s, http.MethodPost, "/v1/memory-suggestions", testToken, map[string]any{"tracker_id": "trk", "key": "other", "action": "summary",
		"reason": "补摘要", "payload": map[string]any{"summary": "另一条"}}, 201, &sg)
	todayCall(t, s, http.MethodPost, "/v1/memory-suggestions/"+jsonNum(sg.Suggestion.ID)+"/dismiss", testToken, nil, 200, nil)
	var listed struct {
		Suggestions []struct {
			State string `json:"state"`
		} `json:"suggestions"`
	}
	todayCall(t, s, http.MethodGet, "/v1/memory-suggestions?state=all", steward, nil, 200, &listed)
	assert.Len(t, listed.Suggestions, 2)
	todayCall(t, s, http.MethodPost, "/v1/memory-suggestions/abc/dismiss", testToken, nil, 400, nil)
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }
