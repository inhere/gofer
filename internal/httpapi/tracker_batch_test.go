package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/tracker"
)

func postTrackerBatch(t *testing.T, e trackerE2E, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/tracker/issues/batch", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestTrackerBatchClosePartialFailureSyncsBack(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	var ids []string
	for _, title := range []string{"a", "b", "c"} {
		it, err := e.local.CreateIssue(tracker.Issue{Title: title, Type: "task"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
	}
	syncTracker(t, e)
	tid := mustConfig(t, e.local).TrackerID

	code, out := postTrackerBatch(t, e, map[string]any{
		"tracker_id": tid,
		"ids":        []string{ids[0], ids[1], "no-such", ids[0]},
		"set":        map[string]any{"status": "closed", "close_reason": "stale", "add_tags": []string{"swept"}},
	})
	if code != http.StatusOK {
		t.Fatalf("status=%d %v", code, out)
	}
	if out["ok"].(float64) != 2 || out["failed"].(float64) != 1 {
		t.Fatalf("summary=%v", out)
	}
	results := out["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("duplicates must collapse: %v", results)
	}
	if bad := results[2].(map[string]any); bad["id"] != "no-such" || bad["ok"] != false || bad["error"] == "" {
		t.Fatalf("bad result=%v", bad)
	}

	syncTracker(t, e)
	for _, id := range ids[:2] {
		got, err := e.local.Issue(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "closed" || got.CloseReason != "stale" || got.ClosedAt == "" || len(got.Tags) != 1 || got.Tags[0] != "swept" {
			t.Fatalf("%s not closed after sync: %+v", id, got)
		}
	}
	if got, _ := e.local.Issue(ids[2]); got.Status == "closed" {
		t.Fatal("untouched issue closed")
	}

	// A status change away from closed clears the close fields; tags are not duplicated.
	code, out = postTrackerBatch(t, e, map[string]any{"tracker_id": tid, "ids": []string{ids[0]}, "set": map[string]any{"status": "blocked", "add_tags": []string{"swept", "x"}}})
	if code != http.StatusOK || out["ok"].(float64) != 1 {
		t.Fatalf("reopen: %d %v", code, out)
	}
	syncTracker(t, e)
	got, _ := e.local.Issue(ids[0])
	if got.Status != "blocked" || got.CloseReason != "" || got.ClosedAt != "" || len(got.Tags) != 2 {
		t.Fatalf("after reopen: %+v", got)
	}
}

func TestTrackerBatchRejectsBadInput(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	tid := mustConfig(t, e.local).TrackerID
	for name, body := range map[string]map[string]any{
		"no tracker":   {"ids": []string{"x"}, "set": map[string]any{"status": "closed"}},
		"no ids":       {"tracker_id": tid, "ids": []string{}, "set": map[string]any{"status": "closed"}},
		"empty set":    {"tracker_id": tid, "ids": []string{"x"}, "set": map[string]any{}},
		"bad status":   {"tracker_id": tid, "ids": []string{"x"}, "set": map[string]any{"status": "nope"}},
		"reason alone": {"tracker_id": tid, "ids": []string{"x"}, "set": map[string]any{"status": "blocked", "close_reason": "r"}},
	} {
		if code, _ := postTrackerBatch(t, e, body); code != http.StatusBadRequest {
			t.Errorf("%s: status=%d", name, code)
		}
	}
}
