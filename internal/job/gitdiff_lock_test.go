package job

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestRunGitDoesNotTakeOptionalIndexLock pins tools-3wc: gofer's read-only git
// calls must not refresh (and therefore lock + rewrite) the repository index.
// Otherwise a concurrent user command (commit/rebase) intermittently fails with
// "index.lock: File exists", and a child killed by the timeout/cap can leave the
// lock behind.
func TestRunGitDoesNotTakeOptionalIndexLock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	// Control: plain `git status` on a stale-index repo rewrites the index. If this
	// git never does, the property below cannot be observed on this machine.
	ctrl := staleIndexRepo(t)
	ctrlBefore := indexSum(t, ctrl)
	gitIn(t, ctrl, "status", "--porcelain")
	if indexSum(t, ctrl) == ctrlBefore {
		t.Skip("this git does not refresh the index on status; optional locks not observable")
	}

	dir := staleIndexRepo(t)
	before := indexSum(t, dir)
	_ = runGit(context.Background(), dir, 4096, "status", "--porcelain")
	if indexSum(t, dir) != before {
		t.Fatal("runGit rewrote .git/index: read-only git must run with GIT_OPTIONAL_LOCKS=0")
	}
}

// staleIndexRepo creates a one-commit repo whose tracked file has a newer mtime
// than recorded in the index, so a plain `git status` wants to refresh the index.
func staleIndexRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "config", "user.email", "t@example.com")
	gitIn(t, dir, "config", "user.name", "t")
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "a.txt")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	later := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(f, later, later); err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func indexSum(t *testing.T, dir string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
