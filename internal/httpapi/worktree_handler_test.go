package httpapi

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// WT-01 endpoint contract: GET /v1/jobs/{id}/worktree reports the LIVE worktree
// state, DELETE removes it (409 while it is dirty and no force is given), and both
// answer 404 for a job that has no managed worktree. The git fixtures are temp
// repositories — never the real checkout.

// gitTestRun runs a git command in dir, failing the test on error.
func gitTestRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newWorktreeTestServer wires a Server whose "repo" project is a real git checkout
// (temp dir), so a --worktree job can fork a worktree in it.
func newWorktreeTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("worktree endpoint tests require git on PATH: %v", err)
	}
	repo := t.TempDir()
	gitTestRun(t, repo, "init", "-q", "-b", "main")
	gitTestRun(t, repo, "config", "user.email", "gofer-test@example.com")
	gitTestRun(t, repo, "config", "user.name", "gofer-test")
	for name, content := range map[string]string{".gitignore": "tmp/\n", "README.md": "fixture\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitTestRun(t, repo, "add", ".")
	gitTestRun(t, repo, "commit", "-q", "-m", "init")

	root := t.TempDir() // result dirs live OUTSIDE the checkout
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"repo": {
				HostPath:       repo,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil), repo
}

// TestJobWorktreeEndpoints walks the whole surface: a --worktree job reports its
// live worktree, a dirty one refuses removal with 409, a forced removal drops the
// worktree and the branch, and a job without a worktree (or an unknown id) is 404.
func TestJobWorktreeEndpoints(t *testing.T) {
	s, repo := newWorktreeTestServer(t)

	resp := do(t, s, http.MethodPost, "/v1/jobs?wait=1", testToken, map[string]any{
		"project_key": "repo", "agent": "exec", "runner": "local",
		"cmd": []string{"go", "version"}, "timeout_sec": 60, "worktree": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.Status != job.StatusDone {
		t.Fatalf("job status=%s (err=%s), want done", created.Status, created.Error)
	}
	if created.WorktreePath == "" || created.WorktreeBranch == "" {
		t.Fatalf("submitted job has no worktree: %+v", created)
	}

	resp = do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/worktree", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get worktree status=%d, want 200", resp.StatusCode)
	}
	var st job.WorktreeStatus
	decode(t, resp, &st)
	if !st.Exists || st.Branch != created.WorktreeBranch || st.Path != created.WorktreePath {
		t.Fatalf("worktree status = %+v, want the live %s / %s", st, created.WorktreePath, created.WorktreeBranch)
	}
	// A clean worktree with no branch commits is trivially "merged" (nothing to lose).
	if st.Dirty || st.CommitsAhead != 0 || !st.Merged {
		t.Fatalf("clean worktree reported dirty=%v ahead=%d merged=%v", st.Dirty, st.CommitsAhead, st.Merged)
	}

	// An uncommitted leftover must block the removal.
	if err := os.WriteFile(filepath.Join(st.Path, "leftover.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp = do(t, s, http.MethodDelete, "/v1/jobs/"+created.ID+"/worktree", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete of a dirty worktree status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := os.Stat(st.Path); err != nil {
		t.Fatalf("refused delete must leave the worktree: %v", err)
	}

	resp = do(t, s, http.MethodDelete, "/v1/jobs/"+created.ID+"/worktree?force=1&delete_branch=1", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forced delete status=%d, want 200", resp.StatusCode)
	}
	var removed job.WorktreeStatus
	decode(t, resp, &removed)
	if removed.Exists {
		t.Fatalf("removed worktree still reports exists=true: %+v", removed)
	}
	if _, err := os.Stat(st.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still present: %v", err)
	}
	if out := gitTestRun(t, repo, "branch", "--list", st.Branch); out != "" {
		t.Fatalf("branch %s still exists: %q", st.Branch, out)
	}

	// A plain job has no worktree → 404; so is an id nobody knows.
	plain := submitPlainJob(t, s)
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+plain+"/worktree", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("worktree of a plain job status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/jobs/nope/worktree", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// submitPlainJob submits a finished job WITHOUT a worktree and returns its id.
func submitPlainJob(t *testing.T, s *Server) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs?wait=1", testToken, map[string]any{
		"project_key": "repo", "agent": "exec", "runner": "local",
		"cmd": []string{"go", "version"}, "timeout_sec": 60,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit plain job status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	return created.ID
}
