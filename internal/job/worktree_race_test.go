package job

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 并发建 worktree 的瞬时错误重试（gofer-ourk）：主机与容器同时往一个仓库建 worktree 时，
// git 扫 .git/worktrees/* 会读到别的进程刚建了一半的登记而直接 fatal。这类失败几百毫秒后
// 自愈，重试有上限；真正的错误（目录已存在、不是仓库、没有 git）一次就返回。

// worktreeRaceFailure is the message git prints when `worktree add` reads the registration
// another machine is still writing (the 2026-10-23 v0.191.2 verify failure).
const worktreeRaceFailure = "git worktree add --detach /repo/tmp/gofer/wt/job-x: " +
	"fatal: failed to read .git/worktrees/job-y/commondir: No error"

// TestIsWorktreeRaceError pins what counts as the race: the two half-written registration
// reads and a momentarily held index lock. A real refusal must never be retried.
func TestIsWorktreeRaceError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"commondir (the reported failure)", errors.New(worktreeRaceFailure), true},
		{"gitdir", errors.New("git worktree add /p: fatal: failed to read .git/worktrees/job-y/gitdir: No error"), true},
		{"index lock", errors.New("git switch -c gofer/x: fatal: Unable to create '/repo/.git/index.lock': File exists"), true},
		{"nil", nil, false},
		{"worktree dir exists", errors.New("git worktree add /p: fatal: '/p' already exists"), false},
		{"not a checkout", errors.New("git rev-parse --show-toplevel: fatal: not a git repository (or any of the parent directories): .git"), false},
		{"no git", os.ErrNotExist, false},
	}
	for _, tc := range cases {
		if got := isWorktreeRaceError(tc.err); got != tc.want {
			t.Errorf("%s: isWorktreeRaceError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// breakWorktreeRegistration leaves the half-written admin entry of ANOTHER worktree under
// .git/worktrees — what a concurrent `worktree add` looks like from the outside: a gitdir
// pointing at a checkout plus an empty commondir, which git reads and dies on.
func breakWorktreeRegistration(t *testing.T, repo, id string) {
	t.Helper()
	dir := filepath.Join(repo, ".git", "worktrees", id)
	other := filepath.Join(repo, "tmp", "gofer", "wt", "other", ".git")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("create the other worktree's git dir: %v", err)
	}
	writeRepoFile(t, dir, "gitdir", other)
	writeRepoFile(t, dir, "commondir", "")
}

// TestGitWorktreeAddRetriesAConcurrentRegistration drives the retry against REAL git: the
// first attempt hits the half-written registration of another worktree (the exact message
// the host got), the second one runs after that registration is gone and must succeed.
func TestGitWorktreeAddRetriesAConcurrentRegistration(t *testing.T) {
	t.Parallel()
	repo, head := gitRepo(t)
	breakWorktreeRegistration(t, repo, "job-race")
	path := filepath.Join(repo, "tmp", "gofer", "wt", "job-mine")

	attempts := 0
	err := retryWorktreeAdd(t.Context(), func() error {
		attempts++
		if attempts == 2 {
			// The other machine finished: its registration is complete, so the half-written
			// entry this machine tripped over is gone.
			if rerr := os.RemoveAll(filepath.Join(repo, ".git", "worktrees", "job-race")); rerr != nil {
				t.Fatalf("clear the concurrent registration: %v", rerr)
			}
		}
		_, gerr := gitOut(t.Context(), repo, "worktree", "add", "--detach", path, head)
		return gerr
	})
	if err != nil {
		t.Fatalf("retryWorktreeAdd = %v, want nil after the registration cleared", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one race, one success)", attempts)
	}
	// Match the path's tail: on Windows the temp dir may be an 8.3 short name (RUNNER~1)
	// while git lists the long one, so the full string differs though it is one directory.
	tail := "tmp/gofer/wt/" + filepath.Base(path)
	if list := gitOutIn(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(filepath.ToSlash(list), tail) {
		t.Fatalf("the retried worktree add did not register %s:\n%s", path, list)
	}
}

// TestGitWorktreeAddDoesNotRetryRealErrors: a target directory that already exists is a real
// refusal — git fails once and the error is handed back untouched (no retry, no masking).
func TestGitWorktreeAddDoesNotRetryRealErrors(t *testing.T) {
	t.Parallel()
	repo, head := gitRepo(t)
	// A target directory that already holds a file is refused by git itself (an EMPTY dir is
	// accepted and checked out, so it would not exercise the error path).
	existing := filepath.Join(repo, "tmp", "exists")
	writeRepoFile(t, existing, "keep.txt", "not empty\n")

	attempts := 0
	err := retryWorktreeAdd(t.Context(), func() error {
		attempts++
		_, gerr := gitOut(t.Context(), repo, "worktree", "add", "--detach", existing, head)
		return gerr
	})
	if err == nil {
		t.Fatal("worktree add onto an existing directory must fail")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (a real error is not retried)", attempts)
	}
	if isWorktreeRaceError(err) {
		t.Fatalf("already-exists must not look like the registration race: %v", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("the real git refusal must reach the caller unchanged, got %v", err)
	}
}

// TestRetryWorktreeAddGivesUp: the retry is bounded. A race that never clears is still an
// error after worktreeAddAttempts tries, with git's own message intact.
func TestRetryWorktreeAddGivesUp(t *testing.T) {
	t.Parallel()
	race := errors.New(worktreeRaceFailure)
	attempts := 0
	err := retryWorktreeAdd(t.Context(), func() error {
		attempts++
		return race
	})
	if !errors.Is(err, race) {
		t.Fatalf("retryWorktreeAdd = %v, want the original race error back", err)
	}
	if attempts != worktreeAddAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, worktreeAddAttempts)
	}
}

// TestRetryWorktreeAddStopsOnCanceledContext: a cancelled context (server shutdown, the
// worktree timeout) ends the retrying at once instead of sleeping through the backoff.
func TestRetryWorktreeAddStopsOnCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := retryWorktreeAdd(ctx, func() error {
		attempts++
		return errors.New(worktreeRaceFailure)
	})
	if err == nil {
		t.Fatal("a cancelled context must not turn the failure into success")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry after cancellation)", attempts)
	}
}

// TestGitOutEnvKeepsGitMessagesEnglish: the stderr gitOut hands back is matched against
// git's English wording, so git must not follow a Chinese machine locale (a macOS runner
// with git translations answered 「已经存在」 under the zh_CN locale the tests pin).
func TestGitOutEnvKeepsGitMessagesEnglish(t *testing.T) {
	env := gitOutEnv()
	last := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "LANGUAGE=") {
			last = kv
		}
	}
	if last != "LANGUAGE=en" {
		t.Fatalf("the last LANGUAGE in gitOut's environment = %q, want LANGUAGE=en", last)
	}
}
