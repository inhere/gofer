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

// commitFile writes rel (slash-separated, relative to dir) with content and commits it.
func commitFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "--", rel)
	git(t, dir, "commit", "-m", "add "+rel)
	return path
}

// TestMergeBlockingChangesParsesNulRecords pins the `git status --porcelain -z` record
// shape: two status columns, one space, the path — unescaped — and, for a rename or copy,
// the original path as its own following record (git's -z order is new then old). That
// second record must be consumed as an origin, or a later entry is misread as a status
// line (gofer-7lp5).
func TestMergeBlockingChangesParsesNulRecords(t *testing.T) {
	porcelain := strings.Join([]string{
		" M .gofer/tracker/记录 一.jsonl",   // tracker: allowed
		"R  .gofer/tracker/新 名.jsonl",    // renamed inside the tracker:
		".gofer/tracker/旧 名.jsonl",       //   this original path is allowed too
		"R  .gofer/tracker/移入 a.txt",     // renamed into the tracker:
		"a.txt",                          //   the original path is outside
		" M .gofer/tracker/前 -> 后.jsonl", // a space in a path is not a rename:
		"M  说明 文档.txt",                   // outside: blocks
	}, "\x00") + "\x00"

	got := mergeBlockingChanges(porcelain)
	want := []string{"a.txt", "说明 文档.txt"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("blocked = %q, want %q", got, want)
	}
}

// TestMergeBranchTrackerPathsNotQuoted: git's short status C-quotes a path holding
// non-ASCII characters or a space (core.quotePath), so the check whether a change sits
// under .gofer/tracker/ has to run on the real path: such a tracker file must not block
// a merge, and a blocker outside the tracker must be named as written (gofer-7lp5).
func TestMergeBranchTrackerPathsNotQuoted(t *testing.T) {
	main, wt, _ := repoWithWorktree(t)
	// git's default is core.quotePath=true (C-quoted non-ASCII bytes); a machine config
	// may disable it, and the quoted shape is what this test is about.
	git(t, main, "config", "core.quotePath", "true")
	const trackerRel = ".gofer/tracker/问题 清单.jsonl"
	trackerFile := commitFile(t, main, trackerRel, "{\"id\":\"a\"}\n")
	// An uncommitted row: the state a supervision leaves the tracker in mid-merge.
	if err := os.WriteFile(trackerFile, []byte("{\"id\":\"b\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, base, err := MergeBranch(context.Background(), wt, "gofer/j1", false); err != nil {
		t.Fatalf("a tracker path with a space and non-ASCII characters must not block: %v", err)
	} else if base != "main" {
		t.Fatalf("merged into %q, want main", base)
	}

	const outsideRel = "说明 文档.txt"
	outside := commitFile(t, main, outsideRel, "one\n")
	if err := os.WriteFile(outside, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := MergeBranch(context.Background(), wt, "gofer/j1", false)
	if !errors.Is(err, ErrMainNotReady) {
		t.Fatalf("a dirty tracked file outside the tracker must still refuse: err = %v", err)
	}
	if !strings.Contains(err.Error(), outsideRel) {
		t.Fatalf("the refusal must name the path as written (%q): %v", outsideRel, err)
	}
	if strings.Contains(err.Error(), ".gofer/tracker/") {
		t.Fatalf("no tracker path may be listed as blocking: %v", err)
	}
}

// TestMergeBranchRenamedPathsAcrossTracker: a rename leaves two paths, the new one and
// (in the record that follows) the original; both are judged. A rename wholly inside
// .gofer/tracker/ does not block — and with two of them, a parser that did not consume
// the trailing origin record would read it as a dirty path — while a rename whose other
// end is outside blocks, naming that end as written (gofer-7lp5).
func TestMergeBranchRenamedPathsAcrossTracker(t *testing.T) {
	renames := [][2]string{
		{".gofer/tracker/旧 名.jsonl", ".gofer/tracker/新 名.jsonl"},
		{".gofer/tracker/记录 一.jsonl", ".gofer/tracker/记录 二.jsonl"},
	}
	for _, tc := range []struct {
		name    string
		renames [][2]string
		blocked []string
	}{
		{name: "inside tracker", renames: renames},
		{name: "original outside", renames: [][2]string{{"a.txt", ".gofer/tracker/移入 a.txt"}}, blocked: []string{"a.txt"}},
		{name: "new outside", renames: [][2]string{{".gofer/tracker/issues.jsonl", "说明 文档.txt"}}, blocked: []string{"说明 文档.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, wt, _ := repoWithWorktree(t)
			// Status rename detection is what makes this one record pair instead of an add
			// plus a delete; a user config turning it off must not skew the test.
			git(t, main, "config", "status.renames", "true")
			for _, r := range tc.renames {
				commitFile(t, main, r[0], "one\n")
			}
			for _, r := range tc.renames {
				from := filepath.Join(main, filepath.FromSlash(r[0]))
				to := filepath.Join(main, filepath.FromSlash(r[1]))
				if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(from, to); err != nil {
					t.Fatal(err)
				}
			}
			git(t, main, "add", "-A")

			// Judge git's real -z output first: that is the decision under test, and a
			// staged rename inside the tracker is refused by `git merge` itself (the staged
			// destination would be overwritten), which is not the dirty check's business.
			dirty, err := gitStatusPorcelain(context.Background(), main)
			if err != nil {
				t.Fatal(err)
			}
			if got := mergeBlockingChanges(dirty); strings.Join(got, "\n") != strings.Join(tc.blocked, "\n") {
				t.Fatalf("blocked = %q, want %q (status %q)", got, tc.blocked, dirty)
			}
			_, _, err = MergeBranch(context.Background(), wt, "gofer/j1", false)
			if len(tc.blocked) == 0 {
				if errors.Is(err, ErrMainNotReady) {
					t.Fatalf("%v must not be a dirty-main refusal: %v", tc.renames, err)
				}
				return
			}
			if !errors.Is(err, ErrMainNotReady) {
				t.Fatalf("%v must refuse the merge: err = %v", tc.renames, err)
			}
			for _, want := range tc.blocked {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("the refusal must name %q as written: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), ".gofer/tracker/") {
				t.Fatalf("no tracker path may be listed as blocking: %v", err)
			}
		})
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
