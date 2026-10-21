//go:build windows

package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Windows-only: a directory holding a file opened WITHOUT share flags cannot be deleted,
// which is how a running process keeps a checkout on disk in the reported 500. The
// fixture needs syscall.CreateFile, so this lives behind a build tag.

// lockFileForDelete opens path with no sharing so os.Remove/os.RemoveAll on it (and on
// its directory) fails until the handle is closed.
func lockFileForDelete(t *testing.T, path string) {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("utf16 %s: %v", path, err)
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("lock %s: %v", path, err)
	}
	t.Cleanup(func() { _ = syscall.CloseHandle(h) })
}

// TestRemoveWorktreeLeftoverDirIsAWarning is the Windows shape from the issue: git cannot
// delete the directory (a process still holds a file in it) and drops the registration
// anyway. The removal must then SUCCEED, delete the branch as asked and report the
// leftover path as a warning — never a bare 500 that also leaves the branch behind
// (gofer-6zmd).
func TestRemoveWorktreeLeftoverDirIsAWarning(t *testing.T) {
	repo, _ := gitRepo(t)
	const branch = "gofer/leftover"
	path := filepath.Join(repo, filepath.FromSlash(worktreeSubdir), "wt-leftover")
	gitOutIn(t, repo, "worktree", "add", "--detach", path, "HEAD")
	gitOutIn(t, path, "switch", "-c", branch)
	locked := filepath.Join(path, "locked.txt")
	if err := os.WriteFile(locked, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lockFileForDelete(t, locked)

	warnings, err := removeWorktreeWithWarnings(context.Background(),
		&worktreeRef{Path: path, Branch: branch}, true, true)
	if err != nil {
		t.Fatalf("a checkout git could not fully delete must not fail the removal: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], path) {
		t.Fatalf("warnings = %v, want one naming the leftover directory %s", warnings, path)
	}
	if !isDir(path) {
		t.Fatalf("the leftover directory is gone; the fixture did not reproduce the lock")
	}
	// No half state: everything but the locked directory is cleaned up, so the caller
	// sees exactly what survived instead of "branch still there, directory still there".
	if out := gitOutIn(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, path) {
		t.Fatalf("the worktree is still registered:\n%s", out)
	}
	if out := gitOutIn(t, repo, "branch", "--list", branch); out != "" {
		t.Fatalf("branch %s still exists: %q", branch, out)
	}
}
