// Package delivery hands a worktree job's branch over (GIT-02, design
// 2026-10-12-delivery-and-cost-estimate-design.md): merge it into the base branch,
// push it to a remote, or push it and open a pull / merge request on GitHub, GitLab or
// Gitea / Forgejo. It runs on the machine that owns the worktree — the server for a
// local job, the worker (through an internal exec job) otherwise — from a
// self-contained Plan, and never force-pushes.
package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/procattr"
)

// Merge errors (typed so callers map them to 409).
var (
	ErrMergeConflict = errors.New("worktree merge has conflicts")
	ErrMainNotReady  = errors.New("main checkout is not ready for a merge")
	// ErrIndexLocked: the main checkout's .git/index.lock is still held once the wait
	// elapsed. The lock is NEVER deleted — another git process owns it.
	ErrIndexLocked = errors.New("git index.lock is still held")
)

// indexLockWait bounds how long a merge waits for `.git/index.lock` to disappear (a
// concurrent commit / rebase / another merge). A variable so tests can shorten it.
var indexLockWait = 60 * time.Second

// mergeWaitTick is the poll interval while waiting for the index to unlock.
var mergeWaitTick = time.Second

// trackerPathPrefix is the one directory whose uncommitted changes never block a merge:
// issues / plans tracking rows are rewritten constantly while supervising, and git
// leaves them alone unless the merged branch touched them.
const trackerPathPrefix = ".gofer/tracker/"

// MergeConflictError reports a conflict after the main checkout has been restored.
type MergeConflictError struct {
	Files []string
}

func (e *MergeConflictError) Error() string {
	if len(e.Files) == 0 {
		return ErrMergeConflict.Error()
	}
	return fmt.Sprintf("%s: %s", ErrMergeConflict, strings.Join(e.Files, ", "))
}

func (e *MergeConflictError) Unwrap() error { return ErrMergeConflict }

// GitOut runs git in dir and returns its trimmed stdout; a failure carries git's stderr.
func GitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	procattr.Background(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", err
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// MainCheckout returns the main working tree of the repository a linked worktree
// belongs to.
func MainCheckout(ctx context.Context, wtPath string) (string, error) {
	common, err := GitOut(ctx, wtPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(wtPath, common)
	}
	return filepath.Dir(filepath.Clean(common)), nil
}

// BaseBranch is the branch the main checkout of wtPath's repository is on — where a
// merge lands and the default target of a pull request.
func BaseBranch(ctx context.Context, wtPath string) (main, branch string, err error) {
	main, err = MainCheckout(ctx, wtPath)
	if err != nil {
		return "", "", err
	}
	branch, err = GitOut(ctx, main, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch == "" {
		return main, "", fmt.Errorf("%w: main checkout is not on a named branch", ErrMainNotReady)
	}
	return main, branch, nil
}

// MergeBranch merges branch (checked out in the worktree wtPath) into its
// repository's main checkout: --no-ff with a merge commit, or --squash plus one
// commit. The main checkout must have no tracked changes and be on a named branch;
// a conflict is fully undone before the typed error is returned, and uncommitted
// .gofer/tracker/ changes survive the undo. It returns the main checkout and the
// branch merged into.
func MergeBranch(ctx context.Context, wtPath, branch string, squash bool) (main, base string, err error) {
	main, err = MainCheckout(ctx, wtPath)
	if err != nil {
		return "", "", err
	}
	// Tracked changes only: untracked files (the managed worktrees themselves in a
	// repo that does not ignore their directory) are not ours to judge, and git merge
	// itself refuses when one would be overwritten. Uncommitted changes under
	// .gofer/tracker/ are allowed too — that tracker is rewritten throughout a
	// supervision and the merge does not touch it unless the branch changed it.
	if dirty, err := gitStatusPorcelain(ctx, main); err != nil {
		return main, "", err
	} else if blocked := mergeBlockingChanges(dirty); len(blocked) > 0 {
		return main, "", fmt.Errorf("%w: main checkout has uncommitted changes: %s", ErrMainNotReady, strings.Join(blocked, ", "))
	}
	base, err = GitOut(ctx, main, "symbolic-ref", "--short", "HEAD")
	if err != nil || base == "" {
		return main, "", fmt.Errorf("%w: main checkout is not on a named branch", ErrMainNotReady)
	}
	if err := waitIndexUnlocked(ctx, main); err != nil {
		return main, base, err
	}
	args := []string{"merge", "--no-ff", "-m", "gofer: merge " + branch, branch}
	if squash {
		args = []string{"merge", "--squash", branch}
	}
	if _, err := GitOut(ctx, main, args...); err != nil {
		files := mergeConflictFiles(ctx, main)
		undoFailedMerge(ctx, main)
		if len(files) == 0 {
			// No conflicted path: git failed for another reason (no committer
			// identity, a hook, a lock) — say what git said, not "conflicts".
			return main, base, fmt.Errorf("merge %s: %w", branch, err)
		}
		return main, base, &MergeConflictError{Files: files}
	}
	if squash {
		if err := waitIndexUnlocked(ctx, main); err != nil {
			return main, base, err
		}
		if _, err := GitOut(ctx, main, "commit", "-m", "gofer: squash merge "+branch); err != nil {
			undoFailedMerge(ctx, main)
			return main, base, err
		}
	}
	return main, base, nil
}

// gitStatusPorcelain returns `git status --porcelain --untracked-files=no` output RAW,
// without GitOut's TrimSpace: the two status columns are significant (a leading space
// means "not staged"), and trimming it would shift every path by one character.
func gitStatusPorcelain(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=no")
	procattr.Background(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", err
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git status: %s", msg)
	}
	return string(out), nil
}

// mergeBlockingChanges picks the paths out of `git status --porcelain` that must stop a
// merge: everything but .gofer/tracker/. Renamed entries carry two paths; either one
// outside the tracker blocks.
func mergeBlockingChanges(porcelain string) []string {
	var blocked []string
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		paths := []string{p}
		if old, newPath, ok := strings.Cut(p, " -> "); ok {
			paths = []string{old, newPath}
		}
		for _, candidate := range paths {
			candidate = strings.Trim(candidate, "\"")
			if candidate == "" || strings.HasPrefix(candidate, trackerPathPrefix) {
				continue
			}
			blocked = append(blocked, candidate)
		}
	}
	return blocked
}

// waitIndexUnlocked waits until the main checkout has no `.git/index.lock`, polling once
// a second for at most indexLockWait. The lock file is never removed: another git process
// (a commit, a rebase, a second merge) owns it, and deleting it corrupts that operation.
func waitIndexUnlocked(ctx context.Context, main string) error {
	lock, err := GitOut(ctx, main, "rev-parse", "--git-path", "index.lock")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(main, lock)
	}
	deadline := time.Now().Add(indexLockWait)
	for {
		if _, statErr := os.Stat(lock); os.IsNotExist(statErr) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w: %s (waited %s)", ErrIndexLocked, lock, indexLockWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mergeWaitTick):
		}
	}
}

// undoFailedMerge puts the main checkout back to its state before a failed merge
// WITHOUT `git reset --hard`. A merge may legitimately start while .gofer/tracker/
// holds uncommitted rows (mergeBlockingChanges lets those through), and a hard reset
// silently dropped them — gofer lost a freshly created issue that way (gofer-nwyw).
// `merge --abort` restores the merged paths and keeps local changes that were not
// part of the merge; a --squash merge leaves no MERGE_HEAD to abort, so
// `reset --merge` — the same "keep local changes" reset — undoes it instead. Both
// were checked to leave uncommitted tracker rows in place. The hard reset stays only
// as a last resort: a merge left half-applied in the tree would block every later
// merge, while the two cleanup commands above cover every failure this code produces.
func undoFailedMerge(ctx context.Context, main string) {
	if mergeInProgress(ctx, main) {
		if _, err := GitOut(ctx, main, "merge", "--abort"); err == nil {
			return
		}
	}
	if _, err := GitOut(ctx, main, "reset", "--merge", "HEAD"); err == nil {
		return
	}
	_, _ = GitOut(ctx, main, "reset", "--hard", "HEAD")
}

// mergeInProgress reports whether main has a merge to abort (a --squash merge is not
// recorded in MERGE_HEAD, so it cannot be aborted).
func mergeInProgress(ctx context.Context, main string) bool {
	path, err := GitOut(ctx, main, "rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil || path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(main, path)
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

func mergeConflictFiles(ctx context.Context, main string) []string {
	out, err := GitOut(ctx, main, "diff", "--name-only", "--diff-filter=U")
	if err != nil || out == "" {
		return nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
}

// Push pushes local ref src to refs/heads/dst on remote — never forced: a remote
// branch that moved elsewhere is an error naming the rejection. A non-empty proxy
// overrides git's http.proxy for this push only.
func Push(ctx context.Context, dir, remote, src, dst, proxy string) error {
	args := []string{"push", remote, src + ":refs/heads/" + dst}
	if proxy != "" {
		args = append([]string{"-c", "http.proxy=" + proxy}, args...)
	}
	_, err := GitOut(ctx, dir, args...)
	return err
}
