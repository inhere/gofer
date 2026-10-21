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

// trackerDirtyOnly commits .gofer/tracker/issues.jsonl with one row and leaves a
// second row uncommitted: the state a merge may start in while a supervision runs. It
// returns the file and the content the main checkout had before the merge.
func trackerDirtyOnly(t *testing.T, main string) (file, content string) {
	t.Helper()
	dir := filepath.Join(main, ".gofer", "tracker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "issues.jsonl")
	if err := os.WriteFile(file, []byte("{\"id\":\"a\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", ".gofer/tracker/issues.jsonl")
	git(t, main, "commit", "-m", "tracker")
	content = "{\"id\":\"a\"}\n{\"id\":\"uncommitted\"}\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file, content
}

// TestMergeBranchConflictKeepsDirtyTracker: undoing a failed merge restores the merged
// paths but must leave the uncommitted tracker rows alone — a hard reset used to
// revert the file to HEAD and silently drop them (gofer-nwyw). Both a real merge (which
// can be aborted) and a --squash merge (which leaves no MERGE_HEAD) take the same path.
func TestMergeBranchConflictKeepsDirtyTracker(t *testing.T) {
	for _, squash := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "squash"}[squash], func(t *testing.T) {
			main, wt, _ := repoWithWorktree(t)
			file, want := trackerDirtyOnly(t, main)
			// Both sides change a.txt — the branch in the worktree, main in a commit —
			// so the merge conflicts and nothing has landed yet.
			if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("branch\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(t, wt, "commit", "-am", "job edits a")
			if err := os.WriteFile(filepath.Join(main, "a.txt"), []byte("main side\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(t, main, "commit", "-m", "local edit", "--", "a.txt")
			head := git(t, main, "rev-parse", "HEAD")

			_, _, err := MergeBranch(context.Background(), wt, "gofer/j1", squash)
			var conflict *MergeConflictError
			if !errors.As(err, &conflict) || len(conflict.Files) == 0 {
				t.Fatalf("squash=%v: err = %v, want a conflict", squash, err)
			}
			if got, readErr := os.ReadFile(file); readErr != nil || string(got) != want {
				t.Fatalf("squash=%v: the uncommitted tracker row was lost: %q (%v)", squash, got, readErr)
			}
			if got := git(t, main, "rev-parse", "HEAD"); got != head {
				t.Fatalf("squash=%v: the failed merge moved HEAD: %s -> %s", squash, head, got)
			}
			if _, statErr := os.Stat(filepath.Join(main, ".git", "MERGE_HEAD")); statErr == nil {
				t.Fatalf("squash=%v: the failed merge was left in progress", squash)
			}
			if got, readErr := os.ReadFile(filepath.Join(main, "a.txt")); readErr != nil || string(got) != "main side\n" {
				t.Fatalf("squash=%v: the merged path was not restored: %q (%v)", squash, got, readErr)
			}
		})
	}
}

// TestMergeBranchRefusedOverDirtyTrackerKeepsIt: the branch commits tracker rows while
// main holds uncommitted ones, so git refuses the merge up front (no MERGE_HEAD to
// abort). The cleanup must not drop main's rows either — this is the shape the lost
// issue was reported in (gofer-nwyw).
func TestMergeBranchRefusedOverDirtyTrackerKeepsIt(t *testing.T) {
	main, wt, _ := repoWithWorktree(t)
	file, want := trackerDirtyOnly(t, main)
	wtFile := filepath.Join(wt, ".gofer", "tracker", "issues.jsonl")
	if err := os.MkdirAll(filepath.Dir(wtFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtFile, []byte("{\"id\":\"branch\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".gofer/tracker/issues.jsonl")
	git(t, wt, "commit", "-m", "job adds a tracker row")
	head := git(t, main, "rev-parse", "HEAD")

	if _, _, err := MergeBranch(context.Background(), wt, "gofer/j1", false); err == nil {
		t.Fatal("the merge must be refused: the branch would overwrite the dirty tracker")
	}
	if got, readErr := os.ReadFile(file); readErr != nil || string(got) != want {
		t.Fatalf("the uncommitted tracker row was lost: %q (%v)", got, readErr)
	}
	if got := git(t, main, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the refused merge moved HEAD: %s -> %s", head, got)
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
