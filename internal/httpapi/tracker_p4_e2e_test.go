package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/tracker"
)

type trackerE2E struct {
	root   string
	local  *tracker.Store
	srv    *httptest.Server
	server *Server
	meta   *jobstore.Store
}

func newTrackerE2E(t *testing.T) trackerE2E {
	t.Helper()
	root, err := os.MkdirTemp("", "gofer-tracker-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	local, _, err := tracker.Init(root, "p4", true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{Token: "tok"}, Storage: config.StorageConfig{Root: root}, Projects: map[string]config.ProjectConfig{"self": {HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true}}}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, map[string]runner.Runner{localrunner.Name: localrunner.New()}, meta, nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, "tok", false, jobs, eng, projects, agents, nil, nil, nil, nil)
	s.SetTrackerStore(meta)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if err := meta.Close(); err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	t.Cleanup(func() { time.Sleep(2 * time.Second) })
	t.Cleanup(func() { drainJobs(t, jobs) })
	return trackerE2E{root: root, local: local, srv: ts, server: s, meta: meta}
}

func syncTracker(t *testing.T, e trackerE2E) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := tracker.SyncHTTPWithToken(ctx, e.local, e.srv.URL, "tok"); err != nil {
		t.Fatal(err)
	}
}

func TestSyncThreeWayMerge(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	issue, err := e.local.CreateIssue(tracker.Issue{Title: "base", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.local.SetMemory("k", "base", "test", "a"); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	issue.Title = "local"
	issue.Description = "local-desc"
	issue.UpdatedAt = tracker.Now()
	if _, err = e.local.UpdateIssue(issue.ID, tracker.IssuePatch{Title: issue.Title, Actor: "local"}); err != nil {
		t.Fatal(err)
	}
	remote := issue
	remote.Title = "server"
	remote.Status = "blocked"
	remote.Tags = []string{"server"}
	body, _ := json.Marshal(remote)
	req, err := http.NewRequest(http.MethodPut, e.srv.URL+"/v1/tracker/issues/"+issue.ID+"?tracker_id="+mustConfig(t, e.local).TrackerID, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("web issue edit status=%d", resp.StatusCode)
	}
	var got tracker.Issue
	for i := 0; i < 20; i++ {
		syncTracker(t, e)
		got, err = e.local.Issue(issue.ID)
		if err == nil && len(got.Notes) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "blocked" {
		t.Fatalf("status=%q", got.Status)
	}
	if len(got.Notes) == 0 {
		t.Fatal("expected conflict note")
	}
	if _, err := e.local.SetMemory("k", "local-memory", "local", "local"); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	mem, err := e.local.Memory("k")
	if err != nil {
		t.Fatal(err)
	}
	if mem.Content == "" {
		t.Fatal("memory missing")
	}
}

func TestSyncRecordsTrackerRepoMetadata(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	if _, err := e.local.CreateIssue(tracker.Issue{Title: "metadata", Type: "task"}); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	repos, err := e.meta.ListTrackerRepos()
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos=%+v err=%v", repos, err)
	}
	if repos[0].LastSyncAt <= 0 || repos[0].RelPath == "" || repos[0].SyncSummary == "" {
		t.Fatalf("sync metadata missing: %+v", repos[0])
	}
}

func TestSyncOfflineThenCatchUp(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	_, err := e.local.CreateIssue(tracker.Issue{Title: "offline", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.local.SetMemory("offline", "one", "test"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err = tracker.SyncHTTPWithToken(ctx, e.local, "http://127.0.0.1:1", "tok")
	cancel()
	if err == nil {
		t.Fatal("offline sync should fail")
	}
	syncTracker(t, e)
	beforeI, _ := os.ReadFile(filepath.Join(e.local.Dir, "issues.jsonl"))
	beforeM, _ := os.ReadFile(filepath.Join(e.local.Dir, "memories.jsonl"))
	base, _ := os.ReadFile(filepath.Join(e.local.Dir, ".local", "sync-base.jsonl"))
	syncTracker(t, e)
	afterI, _ := os.ReadFile(filepath.Join(e.local.Dir, "issues.jsonl"))
	afterM, _ := os.ReadFile(filepath.Join(e.local.Dir, "memories.jsonl"))
	afterBase, _ := os.ReadFile(filepath.Join(e.local.Dir, ".local", "sync-base.jsonl"))
	if string(beforeI) != string(afterI) || string(beforeM) != string(afterM) || string(base) != string(afterBase) {
		t.Fatal("idempotent sync changed bytes")
	}
}

func TestSyncMemoryTombstone(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	if _, err := e.local.SetMemory("gone", "value", "test"); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	if err := e.local.RemoveMemory("gone"); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	if _, err := e.local.Memory("gone"); err == nil {
		t.Fatal("local rm should remove row")
	}
	// Recreate and let the server create the tombstone; the next sync must remove the local row.
	if _, err := e.local.SetMemory("server-delete", "value", "test"); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	cfg := mustConfig(t, e.local)
	req, err := http.NewRequest(http.MethodDelete, e.srv.URL+"/v1/tracker/memories/server-delete?tracker_id="+cfg.TrackerID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}
	syncTracker(t, e)
	if _, err := e.local.Memory("server-delete"); err == nil {
		t.Fatal("server tombstone should remove local row")
	}
}

func TestTrackerWebEditPartialUpdateAndConflict(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	issue, err := e.local.CreateIssue(tracker.Issue{Title: "keep", Type: "task", Priority: 2})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	cfg := mustConfig(t, e.local)
	body := []byte(`{"status":"blocked","expected_rev":1}`)
	req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/v1/tracker/issues/"+issue.ID+"?tracker_id="+cfg.TrackerID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit status=%d", resp.StatusCode)
	}
	syncTracker(t, e)
	got, err := e.local.Issue(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "keep" || got.Status != "blocked" || got.Priority != 2 {
		t.Fatalf("partial body=%+v", got)
	}
	body = []byte(`{"title":"stale","expected_rev":1}`)
	req, _ = http.NewRequest(http.MethodPut, e.srv.URL+"/v1/tracker/issues/"+issue.ID+"?tracker_id="+cfg.TrackerID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status=%d", resp.StatusCode)
	}
}

func TestSyncPullsWebMemoryEdit(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	if _, err := e.local.SetMemory("pull", "value", "test"); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	cfg := mustConfig(t, e.local)
	req, err := http.NewRequest(http.MethodPut, e.srv.URL+"/v1/tracker/memories/pull?tracker_id="+cfg.TrackerID, bytes.NewReader([]byte(`{"content":"edited","expected_rev":1}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit status=%d", resp.StatusCode)
	}
	syncTracker(t, e)
	got, err := e.local.Memory("pull")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "edited" {
		t.Fatalf("content=%q", got.Content)
	}
}

func TestJobIssueLinkAppendsNotes(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	issue, err := e.local.CreateIssue(tracker.Issue{Title: "job", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	resp := do(t, e.server, http.MethodPost, "/v1/jobs", "tok", job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 10, IssueID: issue.ID, TrackerID: mustConfig(t, e.local).TrackerID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status=%d", resp.StatusCode)
	}
	var jr job.JobResult
	decode(t, resp, &jr)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if jr.Status == job.StatusDone || jr.Status == job.StatusFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
		r := do(t, e.server, http.MethodGet, "/v1/jobs/"+jr.ID, "tok", nil)
		decode(t, r, &jr)
	}
	syncTracker(t, e)
	got, err := e.local.Issue(issue.ID)
	if err != nil || len(got.Notes) == 0 {
		t.Fatalf("linked notes missing: err=%v issue=%+v", err, got)
	}
	if got.Status != "in_progress" {
		t.Fatalf("linked status=%q", got.Status)
	}
}

func TestIssueLinkRejectedSubmitDoesNotChangeIssue(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	issue, err := e.local.CreateIssue(tracker.Issue{Title: "reject", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	resp := do(t, e.server, http.MethodPost, "/v1/jobs", "tok", job.JobRequest{ProjectKey: "missing", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, IssueID: issue.ID, TrackerID: mustConfig(t, e.local).TrackerID})
	if resp.StatusCode < 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, err := e.local.Issue(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "open" {
		t.Fatalf("rejected submit changed status=%q", got.Status)
	}
}

func mustConfig(t *testing.T, s *tracker.Store) tracker.Config {
	c, err := s.ReadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// X1: `dep rm`, `--untag` and `issue comment` must survive a round trip through
// the server mirror (a set union would resurrect the removed dep / tag), and the
// mirror body carries the new fields.
func TestSyncKeepsRemovedDepsAndTagsAndSyncsComments(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	a, err := e.local.CreateIssue(tracker.Issue{Title: "A", Type: "task", Tags: []string{"x", "y"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.local.CreateIssue(tracker.Issue{Title: "B", Type: "task", Deps: []tracker.Dep{{ID: a.ID, Type: "blocks"}}})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	if _, err := e.local.RemoveDep(b.ID, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.local.UpdateIssue(a.ID, tracker.IssuePatch{Untag: []string{"y"}, Actor: "me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.local.AddComment(a.ID, "hello", "me"); err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	syncTracker(t, e)
	gotA, _ := e.local.Issue(a.ID)
	gotB, _ := e.local.Issue(b.ID)
	if len(gotA.Tags) != 1 || gotA.Tags[0] != "x" || len(gotA.Comments) != 1 || len(gotB.Deps) != 0 {
		t.Fatalf("local after sync: tags=%v comments=%v deps=%v", gotA.Tags, gotA.Comments, gotB.Deps)
	}
	records, err := e.meta.ListTrackerIssues(mustConfig(t, e.local).TrackerID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range records {
		if rec.ID != a.ID {
			continue
		}
		found = true
		var mirrored tracker.Issue
		if err := json.Unmarshal(rec.Body, &mirrored); err != nil {
			t.Fatal(err)
		}
		if len(mirrored.Tags) != 1 || len(mirrored.Comments) != 1 || mirrored.Comments[0].Text != "hello" {
			t.Fatalf("mirror body: %+v", mirrored)
		}
	}
	if !found {
		t.Fatal("issue missing from the mirror")
	}
}
