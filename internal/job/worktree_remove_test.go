package job

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// `job worktree rm` 的两个边界（gofer-6zmd）：正在被运行中 job 使用的 worktree 拒绝删除
// （含自动续接出来的新 job —— 它继承源 job 的同一目录），以及目录删不掉时不要留下
// "目录在、分支没删"的半状态。

// TestRemoveWorktreeRefusedWhileJobRuns: a continuation job records its source's
// worktree, so the source being finished says nothing about who owns the checkout —
// the removal is refused (even with force), names the live job, deletes nothing, and
// goes through once that job is finished.
func TestRemoveWorktreeRefusedWhileJobRuns(t *testing.T) {
	repo, _ := gitRepo(t)
	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone || final.WorktreePath == "" {
		t.Fatalf("status = %s (err=%s), worktree = %q", final.Status, final.Error, final.WorktreePath)
	}

	// A continuation job (auto-resume / todo continue) continues in the SAME checkout.
	cont := jobstore.JobRecord{
		ID: "cont-live", ProjectKey: "repo", Agent: "exec", Runner: builtinLocalRunner,
		Status: StatusRunning, Cwd: final.Cwd, ResultDir: filepath.Join(state, "cont-live"),
		RequestJSON: `{}`, StartedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
		WorktreePath: final.WorktreePath, WorktreeBranch: final.WorktreeBranch,
		WorktreeBaseSHA: final.WorktreeBaseSHA,
	}
	if err := s.Meta().UpsertJob(cont); err != nil {
		t.Fatalf("seed the live continuation job: %v", err)
	}

	_, err := s.RemoveWorktree(final.ID, true, true)
	if !errors.Is(err, ErrWorktreeInUse) {
		t.Fatalf("remove error = %v, want ErrWorktreeInUse", err)
	}
	if !strings.Contains(err.Error(), "cont-live") || !strings.Contains(err.Error(), StatusRunning) {
		t.Fatalf("error = %q, want it to name the live job and its status", err)
	}
	if !isDir(final.WorktreePath) {
		t.Fatalf("refused removal deleted the checkout %s", final.WorktreePath)
	}
	if out := gitOutIn(t, repo, "branch", "--list", final.WorktreeBranch); out == "" {
		t.Fatalf("refused removal deleted the branch %s", final.WorktreeBranch)
	}

	// The same removal is allowed the moment nothing works in the checkout anymore.
	cont.Status = StatusDone
	if err := s.Meta().UpsertJob(cont); err != nil {
		t.Fatalf("finish the continuation job: %v", err)
	}
	removed, err := s.RemoveWorktree(final.ID, true, true)
	if err != nil {
		t.Fatalf("remove after the live job finished: %v", err)
	}
	if removed.Exists || len(removed.Warnings) != 0 {
		t.Fatalf("removal = %+v, want the checkout gone with no warning", removed)
	}
	if isDir(final.WorktreePath) {
		t.Fatalf("checkout %s still on disk", final.WorktreePath)
	}
	if out := gitOutIn(t, repo, "branch", "--list", final.WorktreeBranch); out != "" {
		t.Fatalf("branch %s still exists: %q", final.WorktreeBranch, out)
	}
}

// TestRemoveWorktreeBusyCheckoutIsRefused: when git cannot remove a checkout that is
// STILL REGISTERED, nothing may be deleted (a half state — directory present, branch
// gone — is the failure this replaces); the answer is ErrWorktreeBusy, which the HTTP
// layer serves as a 409 with the reason.
func TestRemoveWorktreeBusyCheckoutIsRefused(t *testing.T) {
	repo, _ := gitRepo(t)
	path := filepath.Join(repo, filepath.FromSlash(worktreeSubdir), "wt-busy")
	gitOutIn(t, repo, "worktree", "add", "--detach", path, "HEAD")

	_, err := finishCheckoutRemoval(context.Background(), repo, &worktreeRef{Path: path}, false,
		errors.New("git worktree remove: failed to delete"))
	if !errors.Is(err, ErrWorktreeBusy) {
		t.Fatalf("error = %v, want ErrWorktreeBusy", err)
	}
	if !isDir(path) {
		t.Fatalf("a refused removal deleted the checkout %s", path)
	}
}
