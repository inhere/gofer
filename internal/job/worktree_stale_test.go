package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestSweepStaleTempCheckouts pins the startup cleanup of leftover `ref:` checkouts
// (design 2026-10-21 §一·3): a directory no live job owns is removed with the finish
// path's cleanup (unlink, `worktree remove --force`, `worktree prune`), while a checkout
// whose job row is still non-terminal is kept. A finished row's checkout goes.
func TestSweepStaleTempCheckouts(t *testing.T) {
	repo, head := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	root := filepath.Join(repo, filepath.FromSlash(worktreeSubdir))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// One real (registered) temporary checkout and one plain stale directory.
	gitOutIn(t, repo, "worktree", "add", "--detach", filepath.Join(root, "job-stale"), head)
	if err := os.MkdirAll(filepath.Join(root, "job-plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A live job owns this one: its row is still running, so it must survive.
	live := filepath.Join(root, "job-live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "job-live", Status: StatusRunning, Cwd: ".", ResultDir: filepath.Join(t.TempDir(), "live"),
	}); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}

	if n := s.SweepStaleTempCheckouts(t.Context()); n != 2 {
		t.Fatalf("swept %d checkout(s), want 2 (the stale ones only)", n)
	}
	if isDir(filepath.Join(root, "job-stale")) || isDir(filepath.Join(root, "job-plain")) {
		t.Fatal("a stale checkout survived the sweep")
	}
	if !isDir(live) {
		t.Fatal("the sweep removed a running job's checkout")
	}
	// The worked-up repo must not still list the removed checkout.
	if out := gitOutIn(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, filepath.Join(root, "job-stale")) {
		t.Fatalf("worktree list still names the swept checkout:\n%s", out)
	}
	// Once the job's row is terminal, its checkout is stale too.
	done, _, err := s.meta.GetJob("job-live")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	done.Status = StatusDone
	if err := s.meta.UpsertJob(done); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if n := s.SweepStaleTempCheckouts(t.Context()); n != 1 {
		t.Fatalf("swept %d checkout(s) after the job ended, want 1", n)
	}
	if isDir(live) {
		t.Fatal("a finished job's checkout survived the sweep")
	}
}
