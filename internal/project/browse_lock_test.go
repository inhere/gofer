package project

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestRunGitRODoesNotTakeOptionalIndexLock pins tools-3wc for the web console's
// read-only git surface (project git status / repo discovery): polling a project
// must never lock or rewrite its index, or the user's own commit/rebase in that
// repo intermittently fails with "index.lock: File exists".
func TestRunGitRODoesNotTakeOptionalIndexLock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	// Control: plain `git status` on a stale-index repo rewrites the index. If this
	// git never does, the property below cannot be observed on this machine.
	ctrl := staleIndexRepoRO(t)
	ctrlBefore := indexSumRO(t, ctrl)
	gitInRO(t, ctrl, "status", "--porcelain")
	if indexSumRO(t, ctrl) == ctrlBefore {
		t.Skip("this git does not refresh the index on status; optional locks not observable")
	}

	dir := staleIndexRepoRO(t)
	before := indexSumRO(t, dir)
	if _, err := runGitRO(dir, 4096, "status", "--porcelain"); err != nil {
		t.Fatalf("runGitRO status: %v", err)
	}
	if indexSumRO(t, dir) != before {
		t.Fatal("runGitRO rewrote .git/index: read-only git must run with GIT_OPTIONAL_LOCKS=0")
	}
}

// staleIndexRepoRO creates a one-commit repo whose tracked file has a newer mtime
// than recorded in the index, so a plain `git status` wants to refresh the index.
func staleIndexRepoRO(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInRO(t, dir, "init", "-q")
	gitInRO(t, dir, "config", "user.email", "t@example.com")
	gitInRO(t, dir, "config", "user.name", "t")
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInRO(t, dir, "add", "a.txt")
	gitInRO(t, dir, "commit", "-q", "-m", "init")
	later := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(f, later, later); err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitInRO(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func indexSumRO(t *testing.T, dir string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
