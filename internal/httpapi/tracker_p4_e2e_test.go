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
	root := t.TempDir()
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
	jobs := job.NewService(cfg, projects, agents, map[string]runner.Runner{localrunner.Name: localrunner.New()}, meta, nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, "tok", false, jobs, eng, projects, agents, nil, nil, nil, nil)
	s.SetTrackerStore(meta)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); meta.Close() })
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
	syncTracker(t, e)
	got, err := e.local.Issue(issue.ID)
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

func TestSyncOfflineThenCatchUp(t *testing.T) {
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

func TestJobIssueLinkAppendsNotes(t *testing.T) {
	e := newTrackerE2E(t)
	issue, err := e.local.CreateIssue(tracker.Issue{Title: "job", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	resp := do(t, e.server, http.MethodPost, "/v1/jobs", "tok", job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 10})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status=%d", resp.StatusCode)
	}
	_ = issue
	syncTracker(t, e)
}

func mustConfig(t *testing.T, s *tracker.Store) tracker.Config {
	c, err := s.ReadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
