package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// leftoverWorktreeJob submits a worktree job, then replaces its checkout with a plain
// folder holding one file (what a half-failed cleanup leaves behind).
func leftoverWorktreeJob(t *testing.T) (*Service, JobResult, string) {
	t.Helper()
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	j := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "status"}, Cwd: ".", TimeoutSec: 60, Worktree: true,
	})
	if err := os.RemoveAll(j.WorktreePath); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}
	if err := os.MkdirAll(j.WorktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.WorktreePath, "precious.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return s, j, repo
}

// TestWorktreeStatusLeftoverDirectoryIsGone: a folder that is no longer the branch's
// checkout reads as state=gone with no live state probed.
func TestWorktreeStatusLeftoverDirectoryIsGone(t *testing.T) {
	s, j, _ := leftoverWorktreeJob(t)
	st, err := s.WorktreeStatus(j.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != WorktreeStateGone || st.Exists || st.Dirty || st.Merged {
		t.Fatalf("status = %+v, want state=gone, exists=false, no live flags", st)
	}
	if st.HeadSHA != j.WorktreeHeadSHA || st.CommitsAhead != j.CommitsAhead {
		t.Fatalf("status head/ahead = %q/%d, want the recorded %q/%d", st.HeadSHA, st.CommitsAhead, j.WorktreeHeadSHA, j.CommitsAhead)
	}
	if _, err := s.RemoveWorktree(j.ID, true, true); err == nil {
		t.Fatal("rm of a leftover directory succeeded")
	}
	if _, err := os.Stat(filepath.Join(j.WorktreePath, "precious.txt")); err != nil {
		t.Fatalf("leftover file was touched: %v", err)
	}
}

// TestWorktreeStatusLiveCheckoutHasNoState: a real checkout keeps exists=true.
func TestWorktreeStatusLiveCheckoutHasNoState(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	j := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "status"}, Cwd: ".", TimeoutSec: 60, Worktree: true,
	})
	st, err := s.WorktreeStatus(j.ID)
	if err != nil || !st.Exists || st.State != "" {
		t.Fatalf("live status = %+v err=%v", st, err)
	}
}

// TestRetireWorktreeKeepsLeftoverDirectoryAndRegistration: the unattended retention
// sweep never deletes a folder git does not see as the branch's checkout, and leaves
// the git worktree registration alone.
func TestRetireWorktreeKeepsLeftoverDirectoryAndRegistration(t *testing.T) {
	s, j, repo := leftoverWorktreeJob(t)
	// The scenario that matters: nothing untracked to make the probe say "dirty", so
	// only the checkout check can keep the folder (here git resolves it to the main
	// repository, which is clean and has the branch merged).
	if err := os.Remove(filepath.Join(j.WorktreePath, "precious.txt")); err != nil {
		t.Fatal(err)
	}
	s.retireWorktree(jobstore.WorktreeRecord{JobID: j.ID, ProjectKey: j.ProjectKey, Path: j.WorktreePath, Branch: j.WorktreeBranch, BaseSHA: j.WorktreeBaseSHA})
	if !isDir(j.WorktreePath) {
		t.Fatal("leftover directory deleted")
	}
	list := gitOutIn(t, repo, "worktree", "list", "--porcelain")
	if !strings.Contains(filepath.ToSlash(list), filepath.ToSlash(filepath.Base(j.WorktreePath))) {
		t.Fatalf("git registration dropped:\n%s", list)
	}
	if out := gitOutIn(t, repo, "branch", "--list", j.WorktreeBranch); out == "" {
		t.Fatal("branch deleted")
	}
}
