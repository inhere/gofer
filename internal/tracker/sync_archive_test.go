package tracker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// An archive tombstone from the server (P4: a person adopted the steward's archive
// suggestion) moves the local copy into memories-archive.jsonl; a plain tombstone deletes.
func TestSyncArchiveTombstoneArchivesLocally(t *testing.T) {
	s, _, err := Init(t.TempDir(), "arc", true)
	assert.NoErr(t, err)
	_, err = s.SetMemory("old-note", "stale content", "me")
	assert.NoErr(t, err)
	_, err = s.SetMemory("gone-note", "plain delete", "me")
	assert.NoErr(t, err)
	_, err = s.SetMemory("keep", "still true", "me")
	assert.NoErr(t, err)

	phase := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Memories []struct {
				ID string `json:"id"`
			} `json:"memories"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		accepted := map[string]int64{}
		for _, m := range body.Memories {
			accepted[m.ID] = 1
		}
		resp := map[string]any{"issue_cursor": 0, "memory_cursor": 1, "issues": []any{}, "memories": []any{},
			"accepted": map[string]any{"memories": accepted}}
		if phase == 1 {
			resp["memory_cursor"] = 3
			resp["memories"] = []any{
				map[string]any{"id": "old-note", "body": map[string]any{"archive_reason": "90 天未更新"}, "rev": 2, "deleted": true,
					"deleted_at": "2099-01-01T00:00:00Z", "deleted_by": ArchiveTombstonePrefix + "human:alice"},
				map[string]any{"id": "gone-note", "body": map[string]any{}, "rev": 2, "deleted": true,
					"deleted_at": "2099-01-01T00:00:00Z", "deleted_by": "human:alice"},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	ctx := context.Background()
	_, err = SyncHTTP(ctx, s, ts.URL)
	assert.NoErr(t, err)
	phase = 1
	rep, err := SyncHTTP(ctx, s, ts.URL)
	assert.NoErr(t, err)
	assert.Eq(t, 1, rep.Archived)

	live, err := s.ReadMemories()
	assert.NoErr(t, err)
	assert.Len(t, live, 1)
	assert.Eq(t, "keep", live[0].Key)

	archived, err := s.ReadArchivedMemories()
	assert.NoErr(t, err)
	assert.Len(t, archived, 1)
	assert.Eq(t, "old-note", archived[0].Key)
	assert.Eq(t, "stale content", archived[0].Content)
	assert.Eq(t, "human:alice", archived[0].ArchivedBy)
	assert.Eq(t, "90 天未更新", archived[0].ArchiveReason)
}
