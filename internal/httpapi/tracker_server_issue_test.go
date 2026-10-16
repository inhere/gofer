package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

func e2eJSON(t *testing.T, e trackerE2E, method, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(raw))
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

// serverCreateIssue files an issue through the new route and returns its id.
func serverCreateIssue(t *testing.T, e trackerE2E, title string) string {
	t.Helper()
	code, out := e2eJSON(t, e, http.MethodPost, "/v1/tracker/repos/"+mustConfig(t, e.local).TrackerID+"/issues", map[string]any{"title": title, "description": "from the server"})
	if code != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", code, out)
	}
	issue, _ := out["issue"].(map[string]any)
	id, _ := issue["id"].(string)
	if id == "" || out["server_created"] != true {
		t.Fatalf("create body=%v", out)
	}
	return id
}

func localIssues(t *testing.T, e trackerE2E) map[string]tracker.Issue {
	t.Helper()
	items, err := e.local.ReadIssues()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]tracker.Issue{}
	for _, it := range items {
		m[it.ID] = it
	}
	return m
}

// A repository that drew the same id as a server-created, never-pulled issue must not be
// merged with it field by field (the old behaviour: one issue with fields of both).
func TestTrackerServerIssueIDCollisionKeepsBoth(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	if _, err := e.local.CreateIssue(tracker.Issue{Title: "base", Type: "task"}); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e) // registers the repo + prefix and writes sync-revs.json
	cfg := mustConfig(t, e.local)
	id := serverCreateIssue(t, e, "server side")
	// The repository allocates the same id for a different issue before pulling.
	if err := e.local.UpdateIssues(func(items []tracker.Issue) ([]tracker.Issue, error) {
		now := tracker.Now()
		return append(items, tracker.Issue{ID: id, Title: "repo side", Type: "bug", Status: "open", Priority: 1, Description: "from the repository", CreatedAt: now, UpdatedAt: now}), nil
	}); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	got := localIssues(t, e)
	repoSide, ok := got[id]
	if !ok || repoSide.Title != "repo side" || repoSide.Description != "from the repository" {
		t.Fatalf("repo issue %s lost or merged: %+v", id, repoSide)
	}
	var moved tracker.Issue
	for gid, it := range got {
		if gid != id && it.Title == "server side" {
			moved = it
		}
	}
	if moved.ID == "" || moved.Description != "from the server" {
		t.Fatalf("server issue did not survive under a new id: %+v", got)
	}
	if len(moved.Notes) == 0 || !strings.Contains(moved.Notes[len(moved.Notes)-1].Text, "renamed from "+id) {
		t.Fatalf("rename not recorded in notes: %+v", moved.Notes)
	}
	// The mirror holds the same two records, and the pushed one is not flagged.
	mirror, err := e.meta.ListTrackerIssues(cfg.TrackerID, 0)
	if err != nil {
		t.Fatal(err)
	}
	flag := map[string]bool{}
	for _, m := range mirror {
		flag[m.ID] = m.ServerCreated
	}
	if len(mirror) != 3 || flag[id] || !flag[moved.ID] {
		t.Fatalf("mirror=%v", flag)
	}
	// A further sync is stable and, having pulled the renamed record, clears its flag.
	syncTracker(t, e)
	syncTracker(t, e)
	if again := localIssues(t, e); len(again) != 3 {
		t.Fatalf("not stable: %d issues", len(again))
	}
	mirror, _ = e.meta.ListTrackerIssues(cfg.TrackerID, 0)
	for _, m := range mirror {
		if m.ServerCreated {
			t.Fatalf("%s still flagged after the repository pulled it", m.ID)
		}
	}
}

func TestTrackerServerIssueAllocationAndPull(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	base, err := e.local.CreateIssue(tracker.Issue{Title: "base", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	cfg := mustConfig(t, e.local)
	re := regexp.MustCompile("^" + regexp.QuoteMeta(cfg.Prefix) + `-[0-9a-z]{4}$`)
	seen := map[string]bool{base.ID: true}
	for i := 0; i < 5; i++ {
		id := serverCreateIssue(t, e, "server issue")
		if !re.MatchString(id) || seen[id] {
			t.Fatalf("bad or duplicate id %q (prefix %q)", id, cfg.Prefix)
		}
		seen[id] = true
	}
	// Not pulled yet: flagged. After the repository syncs it exists locally.
	mirror, _ := e.meta.ListTrackerIssues(cfg.TrackerID, 0)
	flagged := 0
	for _, m := range mirror {
		if m.ServerCreated {
			flagged++
		}
	}
	if flagged != 5 {
		t.Fatalf("flagged=%d", flagged)
	}
	syncTracker(t, e)
	got := localIssues(t, e)
	for id := range seen {
		if _, ok := got[id]; !ok {
			t.Fatalf("issue %s did not reach the repository: %v", id, got)
		}
	}
	if is := got[firstOther(seen, base.ID)]; is.Title != "server issue" || is.Status != "open" || is.Priority != 2 || is.Type != "task" || len(is.Deps) != 0 || is.Parent != "" {
		t.Fatalf("server issue shape %+v", is)
	}
	syncTracker(t, e) // sends the cursor past them
	mirror, _ = e.meta.ListTrackerIssues(cfg.TrackerID, 0)
	for _, m := range mirror {
		if m.ServerCreated {
			t.Fatalf("%s still flagged after a repository pull", m.ID)
		}
	}
}

func firstOther(m map[string]bool, not string) string {
	for k := range m {
		if k != not {
			return k
		}
	}
	return ""
}

func TestTrackerServerIssueCreateRefusals(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	// A repository registered without a prefix: 409 with a clear message.
	if err := e.meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "noprefix", ProjectKey: "self"}); err != nil {
		t.Fatal(err)
	}
	code, out := e2eJSON(t, e, http.MethodPost, "/v1/tracker/repos/noprefix/issues", map[string]any{"title": "x"})
	if code != http.StatusConflict || !strings.Contains(out["detail"].(string), "repo sync") {
		t.Fatalf("no prefix: %d %v", code, out)
	}
	if code, _ = e2eJSON(t, e, http.MethodPost, "/v1/tracker/repos/never-seen/issues", map[string]any{"title": "x"}); code != http.StatusNotFound {
		t.Fatalf("unknown repo: %d", code)
	}
	syncTracker(t, e)
	path := "/v1/tracker/repos/" + mustConfig(t, e.local).TrackerID + "/issues"
	for name, body := range map[string]map[string]any{
		"no title":     {"title": "  "},
		"bad priority": {"title": "x", "priority": 9},
	} {
		if code, _ = e2eJSON(t, e, http.MethodPost, path, body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", name, code)
		}
	}
	if code, _ = e2eJSON(t, e, http.MethodPost, "/v1/tracker/repos/global/issues", map[string]any{"title": "x"}); code != http.StatusBadRequest && code != http.StatusNotFound {
		t.Fatalf("global tracker: %d", code)
	}
}
