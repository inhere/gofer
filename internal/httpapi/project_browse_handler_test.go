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

// newBrowseServer builds a Server whose "self" project's host path is projHost
// (a git work tree set up by the caller), with results isolated under a separate
// temp storage root so they do not pollute the project tree's git state.
func newBrowseServer(t *testing.T, projHost string) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       projHost,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil)
}

// initGitRepo makes dir a git repo (branch main) with a committed README.md.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# proj\nhello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("init", "-b", "main")
	run("add", ".")
	run("commit", "-m", "init")
}

func TestHandleProjectGit_OK(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	s := newBrowseServer(t, repo)

	resp := do(t, s, http.MethodGet, "/v1/projects/self/git", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var st project.GitStatus
	decode(t, resp, &st)
	if !st.IsGitRepo || st.Branch != "main" {
		t.Fatalf("unexpected git status: %+v", st)
	}
	if len(st.RecentCommits) != 1 || st.RecentCommits[0].Subject != "init" {
		t.Fatalf("unexpected commits: %+v", st.RecentCommits)
	}
}

func TestHandleProjectGit_UnknownProject(t *testing.T) {
	s := newBrowseServer(t, t.TempDir())
	resp := do(t, s, http.MethodGet, "/v1/projects/ghost/git", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestHandleListRepos_OK(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	s := newBrowseServer(t, repo)

	resp := do(t, s, http.MethodGet, "/v1/projects/self/repos", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Repos []project.RepoInfo `json:"repos"`
	}
	decode(t, resp, &body)
	if len(body.Repos) != 1 || body.Repos[0].RelPath != "." {
		t.Fatalf("unexpected repos: %+v", body.Repos)
	}
}

func TestHandleListRepos_UnknownProject(t *testing.T) {
	s := newBrowseServer(t, t.TempDir())
	resp := do(t, s, http.MethodGet, "/v1/projects/ghost/repos", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestHandleGetFile_OK(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	s := newBrowseServer(t, repo)

	resp := do(t, s, http.MethodGet, "/v1/projects/self/file?path=README.md", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var fc project.FileContent
	decode(t, resp, &fc)
	if fc.Name != "README.md" || !strings.Contains(fc.Content, "# proj") {
		t.Fatalf("unexpected file content: %+v", fc)
	}
}

func TestHandleGetFile_Forbidden(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	s := newBrowseServer(t, repo)

	resp := do(t, s, http.MethodGet, "/v1/projects/self/file?path=.env", testToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestHandleGetFile_Traversal404(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	s := newBrowseServer(t, repo)

	// Whitelisted basename but ../ escape → SafeJoin reject → 404.
	resp := do(t, s, http.MethodGet, "/v1/projects/self/file?path=../README.md", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for traversal", resp.StatusCode)
	}
}

func TestHandleGetFile_Binary415(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	// Whitelisted name LICENSE with binary (NUL) content → 415.
	if err := os.WriteFile(filepath.Join(repo, "LICENSE"), []byte{0x00, 0x01, 'x'}, 0o644); err != nil {
		t.Fatalf("write LICENSE: %v", err)
	}
	s := newBrowseServer(t, repo)

	resp := do(t, s, http.MethodGet, "/v1/projects/self/file?path=LICENSE", testToken, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d, want 415 for binary", resp.StatusCode)
	}
}

func TestHandleGetFile_MissingPath(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	s := newBrowseServer(t, repo)

	resp := do(t, s, http.MethodGet, "/v1/projects/self/file", testToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for missing path", resp.StatusCode)
	}
}

func TestHandleGetFile_UnknownProject(t *testing.T) {
	s := newBrowseServer(t, t.TempDir())
	resp := do(t, s, http.MethodGet, "/v1/projects/ghost/file?path=README.md", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}
