package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/tracker"
)

func newTrackerSyncServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: "tok-user"}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
	s.SetTrackerStore(s.jobs.Meta())
	// Hermetic: the dispatched job runs the test helper, never the gofer on PATH
	// (which would sync against whatever server that machine runs).
	s.trackerSyncCmd = []string{testcmd.Path(t), "exit", "0"}
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
	var req job.JobRequest
	if err := json.Unmarshal([]byte(got.RequestJSON), &req); err != nil || !slices.Equal(req.Cmd, s.trackerSyncCommand()) {
		t.Fatalf("cmd = %v (err %v), want the configured sync command %v", req.Cmd, err, s.trackerSyncCommand())
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
	// A project_key the server does not know (the tracker's own label) also falls back to the path.
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: "tr-label", ProjectKey: "some-label", RelPath: filepath.ToSlash(filepath.Join(root, "b"))})
	resp = do(t, s, http.MethodPost, "/v1/tracker/repos/tr-label/sync", "tok-user", nil)
	raw = nil
	decode(t, resp, &raw)
	if resp.StatusCode != http.StatusAccepted || raw["project_key"] != "self" || raw["cwd"] != "b" {
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
	linked, err := s.jobs.Submit(job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{testcmd.Path(t), "sleep", "30s"}, Cwd: ".", TimeoutSec: 60, TrackerID: "tr-a", Tags: []string{job.TrackerSyncJobTag}})
	if err != nil {
		t.Fatal(err)
	}
	// Stop the long-running job before TempDir cleanup: on Windows its open log
	// files make RemoveAll fail ("being used by another process").
	t.Cleanup(func() {
		_ = s.jobs.Cancel(linked.ID)
		waitJobTerminal(t, s.jobs, linked.ID, 15*time.Second)
	})
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

func TestTrackerRepoRename(t *testing.T) {
	t.Parallel()
	s, root := newTrackerSyncServer(t)
	old := "0b8f1c52-7f7a-4a3c-9a61-2f1d6c9b7e11"
	short := tracker.ShortTrackerID(old)
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: old, ProjectKey: "self", RelPath: filepath.ToSlash(root)})
	path := "/v1/tracker/repos/" + old + "/rename"

	status := func(tok string, body any, p string) int {
		resp := do(t, s, http.MethodPost, p, tok, body)
		resp.Body.Close()
		return resp.StatusCode
	}
	// bad new id -> 400 (not the derived one), missing -> 400
	if got := status("tok-user", map[string]string{"new_tracker_id": "tracker-0000000000"}, path); got != http.StatusBadRequest {
		t.Fatalf("bad id status=%d", got)
	}
	if got := status("tok-user", map[string]string{}, path); got != http.StatusBadRequest {
		t.Fatalf("empty status=%d", got)
	}

	// A job tied to another tracker -> 403; a job tied to the old id -> 200.
	other, err := s.jobs.Submit(job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{testcmd.Path(t), "sleep", "30s"}, Cwd: ".", TimeoutSec: 60, TrackerID: "tr-other", Tags: []string{job.TrackerSyncJobTag}})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := s.jobs.Submit(job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{testcmd.Path(t), "sleep", "30s"}, Cwd: ".", TimeoutSec: 60, TrackerID: old, Tags: []string{job.TrackerSyncJobTag}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []string{other.ID, linked.ID} {
			_ = s.jobs.Cancel(id)
			waitJobTerminal(t, s.jobs, id, 15*time.Second)
		}
	})
	otherTok := seedJobToken(t, s, other.ID, jobstore.JobCredentialMember, "")
	linkedTok := seedJobToken(t, s, linked.ID, jobstore.JobCredentialMember, "")
	body := map[string]string{"new_tracker_id": short}
	if got := status(otherTok, body, path); got != http.StatusForbidden {
		t.Fatalf("other job status=%d, want 403", got)
	}
	if got := status(linkedTok, body, path); got != http.StatusOK {
		t.Fatalf("linked job status=%d, want 200", got)
	}
	if _, ok := s.findTrackerRepo(short); !ok {
		t.Fatal("repo not renamed")
	}
	// Idempotent for a person; and the old-bound job may now sync under the short id.
	if got := status("tok-user", body, path); got != http.StatusOK {
		t.Fatalf("repeat status=%d", got)
	}
	resp := do(t, s, http.MethodPost, "/v1/tracker/sync", linkedTok, map[string]any{"tracker_id": short, "rel_path": filepath.ToSlash(root)})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-rename sync status=%d, want 200", resp.StatusCode)
	}
	// Both ids present -> 409.
	old2 := "1b8f1c52-7f7a-4a3c-9a61-2f1d6c9b7e22"
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: old2})
	seedRepo(t, s, jobstore.TrackerRepo{TrackerID: tracker.ShortTrackerID(old2)})
	if got := status("tok-user", map[string]string{"new_tracker_id": tracker.ShortTrackerID(old2)}, "/v1/tracker/repos/"+old2+"/rename"); got != http.StatusConflict {
		t.Fatalf("conflict status=%d", got)
	}
}
