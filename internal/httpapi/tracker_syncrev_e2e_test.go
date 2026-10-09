package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// gofer-dgsm: pushes carry the server rev the client last saw, so local edits
// keep reaching the server after the record passed rev 2 (web edits, link
// updates, repeated syncs) instead of being dropped silently.

func syncTrackerReport(t *testing.T, e trackerE2E) tracker.SyncReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := tracker.SyncHTTPWithToken(ctx, e.local, e.srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func serverIssue(t *testing.T, e trackerE2E, id string) (tracker.Issue, int64) {
	t.Helper()
	items, err := e.meta.ListTrackerIssues(mustConfig(t, e.local).TrackerID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == id {
			var issue tracker.Issue
			if err := json.Unmarshal(item.Body, &issue); err != nil {
				t.Fatal(err)
			}
			return issue, item.Rev
		}
	}
	t.Fatalf("server issue %s missing", id)
	return tracker.Issue{}, 0
}

func serverMemory(t *testing.T, e trackerE2E, key string) jobstore.TrackerRecord {
	t.Helper()
	items, err := e.meta.ListTrackerMemories(mustConfig(t, e.local).TrackerID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == key {
			return item
		}
	}
	t.Fatalf("server memory %s missing", key)
	return jobstore.TrackerRecord{}
}

func webIssueEdit(t *testing.T, e trackerE2E, id string, fields map[string]any) {
	t.Helper()
	_, rev := serverIssue(t, e, id)
	fields["expected_rev"] = rev
	body, _ := json.Marshal(fields)
	req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/v1/tracker/issues/"+id+"?tracker_id="+mustConfig(t, e.local).TrackerID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web edit status=%d", resp.StatusCode)
	}
}

func TestSyncLaterLocalEditsReachServer(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	issue, err := e.local.CreateIssue(tracker.Issue{Title: "repro", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	for i := 1; i <= 4; i++ {
		if _, err := e.local.AddComment(issue.ID, fmt.Sprintf("c%d", i), "me"); err != nil {
			t.Fatal(err)
		}
		syncTracker(t, e)
		got, rev := serverIssue(t, e, issue.ID)
		if len(got.Comments) != i || rev != int64(i+1) {
			t.Fatalf("edit %d: server comments=%d rev=%d", i, len(got.Comments), rev)
		}
	}
	// The record is edited twice on the server, then once locally: one sync
	// lands the local comment on top of both web edits.
	webIssueEdit(t, e, issue.ID, map[string]any{"status": "blocked"})
	webIssueEdit(t, e, issue.ID, map[string]any{"priority": 1})
	if _, err := e.local.AddComment(issue.ID, "after-web", "me"); err != nil {
		t.Fatal(err)
	}
	report := syncTrackerReport(t, e)
	if report.Rejected != 1 || len(report.Unresolved) != 0 {
		t.Fatalf("report=%+v", report)
	}
	srv, _ := serverIssue(t, e, issue.ID)
	loc, _ := e.local.Issue(issue.ID)
	if len(srv.Comments) != 5 || srv.Status != "blocked" || srv.Priority != 1 {
		t.Fatalf("server=%+v", srv)
	}
	if len(loc.Comments) != 5 || loc.Status != "blocked" || loc.Priority != 1 {
		t.Fatalf("local=%+v", loc)
	}
}

func TestSyncWebEditThenLocalEditMergesBoth(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	issue, err := e.local.CreateIssue(tracker.Issue{Title: "base", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	webIssueEdit(t, e, issue.ID, map[string]any{"title": "web-title"})
	desc := "local-desc"
	if _, err := e.local.UpdateIssue(issue.ID, tracker.IssuePatch{Description: &desc, Actor: "me"}); err != nil {
		t.Fatal(err)
	}
	report := syncTrackerReport(t, e)
	if report.Rejected != 1 || len(report.Conflicts) != 0 || len(report.Unresolved) != 0 {
		t.Fatalf("report=%+v", report)
	}
	srv, _ := serverIssue(t, e, issue.ID)
	loc, _ := e.local.Issue(issue.ID)
	for name, got := range map[string]tracker.Issue{"server": srv, "local": loc} {
		if got.Title != "web-title" || got.Description != "local-desc" {
			t.Fatalf("%s=%+v", name, got)
		}
	}
	// Nothing left to push: the next sync is a no-op pull.
	if report := syncTrackerReport(t, e); report.Rejected != 0 || report.Summary != "" {
		t.Fatalf("second report=%+v", report)
	}
}

func TestSyncOldStyleRevOnePushGetsConflict(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	issue, err := e.local.CreateIssue(tracker.Issue{Title: "keep", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	webIssueEdit(t, e, issue.ID, map[string]any{"status": "blocked"})
	cfg := mustConfig(t, e.local)
	resp := do(t, e.server, http.MethodPost, "/v1/tracker/sync", "tok", map[string]any{"tracker_id": cfg.TrackerID,
		"issues": []map[string]any{{"id": issue.ID, "body": map[string]any{"id": issue.ID, "title": "old-client"}, "rev": 1}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out struct {
		Accepted struct {
			Issues map[string]int64 `json:"issues"`
		} `json:"accepted"`
		Conflicts struct {
			Issues []jobstore.TrackerRecord `json:"issues"`
		} `json:"conflicts"`
	}
	decode(t, resp, &out)
	if len(out.Accepted.Issues) != 0 || len(out.Conflicts.Issues) != 1 || out.Conflicts.Issues[0].Rev != 2 || out.Conflicts.Issues[0].ID != issue.ID {
		t.Fatalf("response=%+v", out)
	}
	if srv, _ := serverIssue(t, e, issue.ID); srv.Title != "keep" || srv.Status != "blocked" {
		t.Fatalf("stale push was written: %+v", srv)
	}
}

func TestSyncFirstSyncRepairsDivergence(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	a, err := e.local.CreateIssue(tracker.Issue{Title: "A", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.local.CreateIssue(tracker.Issue{Title: "B", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.local.SetMemory("same", "v", "me"); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	cfg := mustConfig(t, e.local)
	// Server-side winner: a web edit of A the local copy never got.
	webIssueEdit(t, e, a.ID, map[string]any{"title": "web-A"})
	time.Sleep(5 * time.Millisecond)
	// Local winner: B edited locally after; an old client "pushed" it (dropped).
	if _, err := e.local.UpdateIssue(b.ID, tracker.IssuePatch{Title: "local-B", Actor: "me"}); err != nil {
		t.Fatal(err)
	}
	// Memories against server tombstones: deleted on the server after the local
	// copy was written (stays deleted) / before the local copy (local wins).
	if err := e.local.UpdateMemories(func(items []tracker.Memory) ([]tracker.Memory, error) {
		return append(items,
			tracker.Memory{Key: "gone", Content: "old copy", UpdatedAt: "2026-08-14T00:00:00Z", By: "me"},
			tracker.Memory{Key: "back", Content: "newer copy", UpdatedAt: "2026-10-08T00:00:00Z", By: "me"}), nil
	}); err != nil {
		t.Fatal(err)
	}
	for key, at := range map[string]string{"gone": "2026-10-07T00:00:00Z", "back": "2026-10-01T00:00:00Z"} {
		if err := e.meta.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: cfg.TrackerID, ID: key, Body: json.RawMessage("{}"), Rev: 3, UpdatedAt: at, Deleted: true, DeletedAt: at, DeletedBy: "web"}); err != nil {
			t.Fatal(err)
		}
	}
	// Old-client state: the base already equals local (so nothing would be
	// pushed) and there is no rev file yet.
	issues, _ := e.local.ReadIssues()
	memories, _ := e.local.ReadMemories()
	baseBytes, _ := json.Marshal(tracker.SyncSnapshot{Issues: issues, Memories: memories})
	if err := os.WriteFile(filepath.Join(e.local.Dir, ".local", "sync-base.jsonl"), append(baseBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.local.Dir, ".local", "sync-revs.json")); err != nil {
		t.Fatal(err)
	}

	report := syncTrackerReport(t, e)
	if report.RepairedToLocal != 2 || report.RepairedToServer != 2 || len(report.Unresolved) != 0 {
		t.Fatalf("report=%+v", report)
	}
	if got, _ := e.local.Issue(a.ID); got.Title != "web-A" {
		t.Fatalf("local A=%+v", got)
	}
	if got, _ := serverIssue(t, e, b.ID); got.Title != "local-B" {
		t.Fatalf("server B=%+v", got)
	}
	if _, err := e.local.Memory("gone"); err == nil {
		t.Fatal("newer server tombstone must remove the local copy")
	}
	if m := serverMemory(t, e, "gone"); !m.Deleted {
		t.Fatalf("server tombstone resurrected: %+v", m)
	}
	if m, err := e.local.Memory("back"); err != nil || m.Content != "newer copy" {
		t.Fatalf("local back=%+v err=%v", m, err)
	}
	if m := serverMemory(t, e, "back"); m.Deleted || m.Rev != 4 {
		t.Fatalf("server back=%+v", m)
	}
	if _, err := os.Stat(filepath.Join(e.local.Dir, ".local", "sync-revs.json")); err != nil {
		t.Fatal("rev file not written")
	}
	// Repair is one-time: the next sync is a plain no-op.
	if report := syncTrackerReport(t, e); report.Summary != "" {
		t.Fatalf("second report=%+v", report)
	}
}
