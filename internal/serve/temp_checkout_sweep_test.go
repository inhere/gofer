package serve

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// TestStartTempCheckoutSweepLoopSweepsOnStart pins serve's wiring of the stale `ref:`
// checkout sweep (gofer-bgkk): the loop runs it once at startup, so a temporary checkout
// a crashed, killed or shutdown-interrupted job left behind is reclaimed on the next boot —
// following the PROJECT's worktree.dir template, which is where the leftovers of a project
// with a custom checkout location live.
func TestStartTempCheckoutSweepLoopSweepsOnStart(t *testing.T) {
	repo := initTempRepo(t)
	cfg := &config.Config{Storage: config.StorageConfig{Root: t.TempDir()}}
	cfg.Projects = map[string]config.ProjectConfig{
		"repo": {
			HostPath: repo,
			Worktree: &config.ProjectWorktree{Dir: ".worktrees/job-{job_id}"},
		},
	}
	config.ApplyDefaults(cfg)
	cr, err := core.Build(cfg, core.WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })

	head := gitIn(t, repo, "rev-parse", "HEAD")
	leftover := filepath.Join(repo, ".worktrees", "job-20260101-000000-abcdef01")
	gitIn(t, repo, "worktree", "add", "--detach", leftover, head)
	if !isDirPath(leftover) {
		t.Fatalf("fixture: %s was not created", leftover)
	}

	stop := make(chan struct{})
	defer close(stop)
	startTempCheckoutSweepLoop(gcli.NewCommand("t", ""), cr.Jobs, stop)

	wait.Until(t, wait.Timeout(t, 10*time.Second), "the leftover temporary checkout is swept on startup", func() bool {
		_, err := os.Stat(leftover)
		return os.IsNotExist(err)
	})
}

// initTempRepo creates a throwaway git checkout with one commit and returns its path.
func initTempRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("this test requires git on PATH: %v", err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "gofer-test@example.com"},
		{"config", "user.name", "gofer-test"},
		{"config", "commit.gpgsign", "false"},
	} {
		gitIn(t, repo, args...)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// gitIn runs one git command in dir and returns its trimmed stdout.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// isDirPath reports whether p exists and is a directory.
func isDirPath(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
