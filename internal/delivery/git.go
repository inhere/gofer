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

	"github.com/inhere/gofer/internal/procattr"
)

// Merge errors (typed so callers map them to 409).
var (
	ErrMergeConflict = errors.New("worktree merge has conflicts")
	ErrMainNotReady  = errors.New("main checkout is not ready for a merge")
)

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
// a conflict is fully undone before the typed error is returned. It returns the main
// checkout and the branch merged into.
func MergeBranch(ctx context.Context, wtPath, branch string, squash bool) (main, base string, err error) {
	main, err = MainCheckout(ctx, wtPath)
	if err != nil {
		return "", "", err
	}
	// Tracked changes only: untracked files (the managed worktrees themselves in a
	// repo that does not ignore their directory) are not ours to judge, and git merge
	// itself refuses when one would be overwritten.
	if dirty, err := GitOut(ctx, main, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return main, "", err
	} else if dirty != "" {
		return main, "", fmt.Errorf("%w: main checkout has uncommitted changes", ErrMainNotReady)
	}
	base, err = GitOut(ctx, main, "symbolic-ref", "--short", "HEAD")
	if err != nil || base == "" {
		return main, "", fmt.Errorf("%w: main checkout is not on a named branch", ErrMainNotReady)
	}
	args := []string{"merge", "--no-ff", "-m", "gofer: merge " + branch, branch}
	if squash {
		args = []string{"merge", "--squash", branch}
	}
	if _, err := GitOut(ctx, main, args...); err != nil {
		files := mergeConflictFiles(ctx, main)
		_, _ = GitOut(ctx, main, "merge", "--abort")
		// The checkout was verified clean above, so a hard reset only discards the
		// half-applied merge (a failed --squash leaves no MERGE_HEAD to abort).
		_, _ = GitOut(ctx, main, "reset", "--hard", "HEAD")
		return main, base, &MergeConflictError{Files: files}
	}
	if squash {
		if _, err := GitOut(ctx, main, "commit", "-m", "gofer: squash merge "+branch); err != nil {
			_, _ = GitOut(ctx, main, "reset", "--hard", "HEAD")
			return main, base, err
		}
	}
	return main, base, nil
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
// branch that moved elsewhere is an error naming the rejection.
func Push(ctx context.Context, dir, remote, src, dst string) error {
	_, err := GitOut(ctx, dir, "push", remote, src+":refs/heads/"+dst)
	return err
}
