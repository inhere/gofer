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
	calls := 0
	var lengths []int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Issues []json.RawMessage `json:"issues"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls++
		lengths = append(lengths, len(body.Issues))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue_cursor":1,"memory_cursor":0,"issues":[],"memories":[]}`))
	}))
	defer ts.Close()
	ctx := context.Background()
	if _, err := SyncHTTP(ctx, s, ts.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncHTTP(ctx, s, ts.URL); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || lengths[1] != 0 {
		t.Fatalf("calls=%d lengths=%v", calls, lengths)
	}
}
