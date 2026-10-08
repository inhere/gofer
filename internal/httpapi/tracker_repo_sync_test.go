package httpapi

import (
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func newTrackerSyncServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: "tok-user"}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
	s.SetTrackerStore(s.jobs.Meta())
	cfg := s.projects.Config()
	return s, cfg.Projects["self"].HostPath
}

func seedRepo(t *testing.T, s *Server, repo jobstore.TrackerRepo) {
	t.Helper()
	if err := s.trackerStore.UpsertTrackerRepo(repo); err != nil {
		t.Fatal(err)
	}
}

func TestTrackerRepoSyncDispatch(t *testing.T) {
	t.Parallel()
	s, root := newTrackerSyncServer(t)
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: "tr-sub", ProjectKey: "self", RelPath: filepath.ToSlash(filepath.Join(root, "sub", "repo")), Prefix: "x"})

	resp := do(t, s, http.MethodPost, "/v1/tracker/repos/tr-sub/sync", "tok-user", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, want 202", resp.StatusCode)
	}
	var out struct {
		JobID, Runner, Cwd string
	}
	var raw map[string]string
	decode(t, resp, &raw)
	out.JobID, out.Runner, out.Cwd = raw["job_id"], raw["runner"], raw["cwd"]
	if out.JobID == "" || out.Runner != "local" || out.Cwd != "sub/repo" {
		t.Fatalf("response = %+v", raw)
	}
	got, ok := s.jobs.Get(out.JobID)
	if !ok {
		t.Fatal("dispatched job not found")
	}
	if got.Agent != "exec" || got.Runner != "local" || got.ProjectKey != "self" || got.TrackerID != "tr-sub" || got.IssueID != "" {
		t.Fatalf("job = %+v", got)
	}
	if !slices.Contains(got.Tags, job.TrackerSyncJobTag) {
		t.Fatalf("tags = %v, want %s", got.Tags, job.TrackerSyncJobTag)
	}
	// Hidden from the default list, visible with all.
	list, _ := s.jobs.ListJobs(job.ListOpts{})
	for _, j := range list {
		if j.ID == out.JobID {
			t.Fatal("tracker-sync job must be hidden from the default list")
		}
	}
	all, _ := s.jobs.ListJobs(job.ListOpts{All: true})
	if !slices.ContainsFunc(all, func(j job.JobResult) bool { return j.ID == out.JobID }) {
		t.Fatal("tracker-sync job must show with --all")
	}
}

func TestTrackerRepoSyncSourceRunnerAndFallback(t *testing.T) {
	t.Parallel()
	s, root := newTrackerSyncServer(t)
	// "server" is the CLI spelling of the built-in runner: it must normalize (G043).
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: "tr-root", ProjectKey: "self", RelPath: filepath.ToSlash(root), SourceRunner: "server"})
	resp := do(t, s, http.MethodPost, "/v1/tracker/repos/tr-root/sync", "tok-user", nil)
	var raw map[string]string
	decode(t, resp, &raw)
	if resp.StatusCode != http.StatusAccepted || raw["runner"] != "local" || raw["cwd"] != "." {
		t.Fatalf("status=%d body=%v", resp.StatusCode, raw)
	}
	// No project_key: matched by path, and the default runner is used (no source).
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: "tr-nokey", RelPath: filepath.ToSlash(filepath.Join(root, "a"))})
	resp = do(t, s, http.MethodPost, "/v1/tracker/repos/tr-nokey/sync", "tok-user", nil)
	raw = nil
	decode(t, resp, &raw)
	if resp.StatusCode != http.StatusAccepted || raw["project_key"] != "self" || raw["runner"] != "local" || raw["cwd"] != "a" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, raw)
	}
}

func TestTrackerRepoSyncRefusals(t *testing.T) {
	t.Parallel()
	s, root := newTrackerSyncServer(t)
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: "tr-out", ProjectKey: "self", RelPath: "/elsewhere/repo"})
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: "tr-none", RelPath: "/elsewhere/repo"})
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: "tr-ok", ProjectKey: "self", RelPath: filepath.ToSlash(root)})

	for _, c := range []struct {
		id   string
		want int
	}{{"missing", http.StatusNotFound}, {"tr-out", http.StatusConflict}, {"tr-none", http.StatusConflict}} {
		resp := do(t, s, http.MethodPost, "/v1/tracker/repos/"+c.id+"/sync", "tok-user", nil)
		if resp.StatusCode != c.want {
			t.Fatalf("%s status=%d, want %d", c.id, resp.StatusCode, c.want)
		}
		resp.Body.Close()
	}

	// A job credential (member) is refused by the route gate; so is a no-token call.
	asking := submitExecJob(t, s, "tok-user")
	tok := seedJobToken(t, s, asking.ID, jobstore.JobCredentialMember, "")
	resp := do(t, s, http.MethodPost, "/v1/tracker/repos/tr-ok/sync", tok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member job status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	stew := seedJobToken(t, s, asking.ID+"-st", jobstore.JobCredentialSteward, "")
	resp = do(t, s, http.MethodPost, "/v1/tracker/repos/tr-ok/sync", stew, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("steward status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/tracker/repos/tr-ok/sync", "", nil)
	if resp.StatusCode == http.StatusAccepted {
		t.Fatal("anonymous caller must not dispatch")
	}
	resp.Body.Close()
}

func TestTrackerSyncJobCredentialScope(t *testing.T) {
	t.Parallel()
	s, root := newTrackerSyncServer(t)
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: "tr-a", ProjectKey: "self", RelPath: filepath.ToSlash(root)})

	// A job tied to tr-a (as the dispatched sync job is) vs. one tied to nothing.
	linked, err := s.jobs.Submit(job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 60, TrackerID: "tr-a", Tags: []string{job.TrackerSyncJobTag}})
	if err != nil {
		t.Fatal(err)
	}
	plain := submitExecJob(t, s, "tok-user")
	linkedTok := seedJobToken(t, s, linked.ID, jobstore.JobCredentialMember, "")
	plainTok := seedJobToken(t, s, plain.ID, jobstore.JobCredentialMember, "")

	post := func(tok, tracker string) int {
		resp := do(t, s, http.MethodPost, "/v1/tracker/sync", tok, map[string]any{"tracker_id": tracker, "rel_path": filepath.ToSlash(root)})
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(plainTok, "tr-a"); got != http.StatusForbidden {
		t.Fatalf("unlinked job status=%d, want 403", got)
	}
	if got := post(linkedTok, "tr-other"); got != http.StatusForbidden {
		t.Fatalf("linked job, other tracker status=%d, want 403", got)
	}
	if got := post(linkedTok, "tr-a"); got != http.StatusOK {
		t.Fatalf("linked job, own tracker status=%d, want 200", got)
	}
	// The job's runner is recorded as the repo's source.
	repos, _ := s.trackerStore.ListTrackerRepos()
	for _, r := range repos {
		if r.TrackerID == "tr-a" && r.SourceRunner != "local" {
			t.Fatalf("source_runner = %q, want local", r.SourceRunner)
		}
	}
	// A human sync keeps the recorded source.
	resp := do(t, s, http.MethodPost, "/v1/tracker/sync", "tok-user", map[string]any{"tracker_id": "tr-a"})
	resp.Body.Close()
	repos, _ = s.trackerStore.ListTrackerRepos()
	for _, r := range repos {
		if r.TrackerID == "tr-a" && r.SourceRunner != "local" {
			t.Fatalf("source_runner lost after a user sync: %q", r.SourceRunner)
		}
	}
}

func TestDefaultProjectRunner(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		allowed []string
		want    string
	}{{nil, "local"}, {[]string{"w1", "server"}, "local"}, {[]string{"w1", "w2"}, "w1"}} {
		if got := defaultProjectRunner(config.ProjectConfig{AllowedRunners: c.allowed}); got != c.want {
			t.Fatalf("allowed=%v got %q want %q", c.allowed, got, c.want)
		}
	}
}
