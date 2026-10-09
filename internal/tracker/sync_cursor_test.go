package tracker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncSecondRequestContainsOnlyDelta(t *testing.T) {
	root := t.TempDir()
	s, _, err := Init(root, "cur", true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateIssue(Issue{Title: "one", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	var lengths []int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Issues []struct {
				ID string `json:"id"`
			} `json:"issues"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lengths = append(lengths, len(body.Issues))
		accepted := map[string]int64{}
		for _, i := range body.Issues {
			accepted[i.ID] = 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issue_cursor": 1, "memory_cursor": 0, "issues": []any{}, "memories": []any{},
			"accepted": map[string]any{"issues": accepted}})
	}))
	defer ts.Close()
	ctx := context.Background()
	if _, err := SyncHTTP(ctx, s, ts.URL); err != nil {
		t.Fatal(err)
	}
	// First sync: full pull (rev map bootstrap), then the push of the new issue.
	if len(lengths) != 2 || lengths[0] != 0 || lengths[1] != 1 {
		t.Fatalf("first sync lengths=%v", lengths)
	}
	if _, err := SyncHTTP(ctx, s, ts.URL); err != nil {
		t.Fatal(err)
	}
	if len(lengths) != 3 || lengths[2] != 0 {
		t.Fatalf("second sync lengths=%v", lengths)
	}
}
