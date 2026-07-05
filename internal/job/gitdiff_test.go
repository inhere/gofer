package job

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// initGitRepo creates a git repo under dir, writes + commits tracked.txt, then
// modifies it (an uncommitted tracked change) so `git diff` is non-empty. It
// skips the test if git is not on PATH. Returns dir for convenience.
func initGitRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping git-diff test")
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Deterministic identity so commit works in a clean CI env.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write tracked: %v", err)
	}
	run("add", "tracked.txt")
	run("commit", "-m", "initial")
	// Uncommitted tracked change → git diff is non-empty.
	if err := os.WriteFile(tracked, []byte("modified content\n"), 0o644); err != nil {
		t.Fatalf("modify tracked: %v", err)
	}
	return dir
}

// TestCaptureDiffGitRepo proves a tracked uncommitted change yields a non-empty
// --stat summary AND a changes.diff dropped into resultDir containing the change.
func TestCaptureDiffGitRepo(t *testing.T) {
	repo := initGitRepo(t, t.TempDir())
	resultDir := t.TempDir()

	summary := captureDiff(repo, resultDir)
	if summary == "" {
		t.Fatalf("captureDiff on a modified git repo returned empty --stat summary")
	}
	if !strings.Contains(summary, "tracked.txt") {
		t.Fatalf("--stat summary should mention the changed file, got %q", summary)
	}

	diffPath := filepath.Join(resultDir, "changes.diff")
	b, err := os.ReadFile(diffPath)
	if err != nil {
		t.Fatalf("changes.diff not written: %v", err)
	}
	full := string(b)
	if !strings.Contains(full, "tracked.txt") || !strings.Contains(full, "modified content") {
		t.Fatalf("changes.diff missing the tracked change, got:\n%s", full)
	}

	// changes.diff must be 0644 on platforms with POSIX mode bits.
	fi, err := os.Stat(diffPath)
	if err != nil {
		t.Fatalf("stat changes.diff: %v", err)
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o644 {
		t.Fatalf("changes.diff perm=%o, want 0644", perm)
	}
}

// TestCaptureDiffNonGit proves a plain (non-git) directory yields "" and writes
// no changes.diff.
func TestCaptureDiffNonGit(t *testing.T) {
	plain := t.TempDir()
	resultDir := t.TempDir()

	if got := captureDiff(plain, resultDir); got != "" {
		t.Fatalf("captureDiff on non-git dir should be \"\", got %q", got)
	}
	if _, err := os.Stat(filepath.Join(resultDir, "changes.diff")); !os.IsNotExist(err) {
		t.Fatalf("non-git dir must not write changes.diff (err=%v)", err)
	}
}

// TestCaptureDiffCleanGitRepo proves a git repo with no uncommitted changes
// yields an empty summary and no changes.diff (full diff is empty → not written).
func TestCaptureDiffCleanGitRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// init + commit, but DO NOT modify afterwards → clean tree.
	repo := initGitRepo(t, dir)
	// Revert the modification so the tree is clean again.
	cmd := exec.Command("git", "checkout", "--", "tracked.txt")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}

	resultDir := t.TempDir()
	if got := captureDiff(repo, resultDir); got != "" {
		t.Fatalf("clean git repo should yield empty summary, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(resultDir, "changes.diff")); !os.IsNotExist(err) {
		t.Fatalf("clean git repo must not write changes.diff (err=%v)", err)
	}
}

// TestRunGitMissingBinary proves runGit degrades gracefully (nil, no panic) when
// "git" cannot be found, by running with a PATH that has no git. isGitWorkTree
// then reports false so the whole capture degrades to "".
func TestRunGitMissingBinary(t *testing.T) {
	// Empty PATH so exec.LookPath("git") inside CommandContext fails to start.
	t.Setenv("PATH", "")
	ctx := context.Background()

	if out := runGit(ctx, t.TempDir(), 256, "rev-parse", "--is-inside-work-tree"); out != nil {
		t.Fatalf("runGit with no git on PATH should return nil, got %q", out)
	}
	if isGitWorkTree(ctx, t.TempDir()) {
		t.Fatalf("isGitWorkTree must be false when git is unavailable")
	}
	if got := captureDiff(t.TempDir(), t.TempDir()); got != "" {
		t.Fatalf("captureDiff must degrade to \"\" when git is unavailable, got %q", got)
	}
}

// TestRunGitTruncatesOutput proves runGit honours the cap by reading at most
// capBytes. We use `git rev-parse --is-inside-work-tree` (output "true\n", 5
// bytes) capped to 2 bytes → "tr".
func TestRunGitTruncatesOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := initGitRepo(t, t.TempDir())
	out := runGit(context.Background(), repo, 2, "rev-parse", "--is-inside-work-tree")
	if len(out) > 2 {
		t.Fatalf("runGit cap=2 returned %d bytes: %q", len(out), out)
	}
}
