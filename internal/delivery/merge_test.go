package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setLockWait shortens the index.lock wait for one test and restores it afterwards.
func setLockWait(t *testing.T, wait, tick time.Duration) {
	t.Helper()
	oldWait, oldTick := indexLockWait, mergeWaitTick
	indexLockWait, mergeWaitTick = wait, tick
	t.Cleanup(func() { indexLockWait, mergeWaitTick = oldWait, oldTick })
}

// TestMergeBranchAllowsTrackerDirtyOnly: uncommitted changes that sit only under
// .gofer/tracker/ do not block a merge (that tracker is rewritten throughout a
// supervision); any other tracked change still refuses, naming the file.
func TestMergeBranchAllowsTrackerDirtyOnly(t *testing.T) {
	main, wt, _ := repoWithWorktree(t)
	tracker := filepath.Join(main, ".gofer", "tracker")
	if err := os.MkdirAll(tracker, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tracker, "issues.jsonl")
	if err := os.WriteFile(file, []byte("{\"id\":\"a\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", ".gofer/tracker/issues.jsonl")
	git(t, main, "commit", "-m", "tracker")
	if err := os.WriteFile(file, []byte("{\"id\":\"b\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, base, err := MergeBranch(context.Background(), wt, "gofer/j1", false); err != nil {
		t.Fatalf("tracker-only changes must not block the merge: %v", err)
	} else if base != "main" {
		t.Fatalf("merged into %q, want main", base)
	}

	// A tracked file OUTSIDE the tracker still refuses, and the error lists it.
	if err := os.WriteFile(filepath.Join(main, "a.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := MergeBranch(context.Background(), wt, "gofer/j1", false)
	if !errors.Is(err, ErrMainNotReady) {
		t.Fatalf("other dirty files must still refuse: err = %v", err)
	}
	if !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("the refusal must name the blocking file: %v", err)
	}
}

// TestMergeBranchWaitsForIndexLock: a lock that disappears within the wait does not
// fail the merge.
func TestMergeBranchWaitsForIndexLock(t *testing.T) {
	main, wt, _ := repoWithWorktree(t)
	setLockWait(t, 5*time.Second, 50*time.Millisecond)
	lock := filepath.Join(main, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = os.Remove(lock)
		close(released)
	}()
	if _, _, err := MergeBranch(context.Background(), wt, "gofer/j1", false); err != nil {
		t.Fatalf("merge after the lock was released: %v", err)
	}
	<-released
}

// TestMergeBranchIndexLockTimeout: a lock that stays held fails with the typed error
// after the wait, and the lock file is left alone (another git process owns it).
func TestMergeBranchIndexLockTimeout(t *testing.T) {
	main, wt, _ := repoWithWorktree(t)
	setLockWait(t, 300*time.Millisecond, 50*time.Millisecond)
	lock := filepath.Join(main, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := MergeBranch(context.Background(), wt, "gofer/j1", false)
	if !errors.Is(err, ErrIndexLocked) {
		t.Fatalf("err = %v, want ErrIndexLocked", err)
	}
	if _, statErr := os.Stat(lock); statErr != nil {
		t.Fatalf("the lock must never be deleted: %v", statErr)
	}
	if out := git(t, main, "branch", "--list", "gofer/j1"); out == "" {
		t.Fatal("a timed-out merge must not touch the branch")
	}
}
