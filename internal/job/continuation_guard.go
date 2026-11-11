package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
)

// Continuation worktree guard (gofer-fi7f): every entry that "carries on" a worktree
// job (resume, auto-resume, todo continuation, takeover, retry, ...) either runs inside
// that job's live worktree or is refused. It never falls back to the main checkout.

const (
	// FailureClassWorktreeGone marks a job that was refused (at submit or just before
	// it ran) because the worktree it was to continue in is gone. Nothing takes it over:
	// a retry, resume or takeover would meet the same missing worktree.
	FailureClassWorktreeGone = "worktree-gone"

	// EventJobContinuationSkipped is recorded on the SOURCE job when an automatic
	// continuation was not submitted because its worktree is gone; detail
	// {entry, reason, worktree_path}.
	EventJobContinuationSkipped = "job.continuation_skipped"

	// Reasons of a ContinuationWorktreeError.
	reasonWorktreeGone           = "worktree_gone"
	reasonWorktreeOutsideProject = "worktree_outside_project"

	// worktreeInUseFormat is the ErrWorktreeInUse text of a merge / merge cleanup that
	// finds a live job in the worktree: path, then the job list.
	worktreeInUseFormat = "%w: %s is used by %s; wait for them to finish or cancel them"
)

// ErrContinuationWorktreeGone: the worktree job being carried on has no usable worktree
// any more. The HTTP layer maps it to 409. Its TEXT is a sentinel too: a persisted
// failure carries text only (see failureDecision).
var ErrContinuationWorktreeGone = errors.New("continuation worktree gone")

// ContinuationWorktreeError is ErrContinuationWorktreeGone with the reason and path.
type ContinuationWorktreeError struct {
	Reason string
	Path   string
}

func (e *ContinuationWorktreeError) Error() string {
	where := ""
	if e.Path != "" { // a cleaned-up worktree leaves no path on the source row
		where = ": " + e.Path
	}
	return ErrContinuationWorktreeGone.Error() + " (" + e.Reason + ")" + where +
		"; the work is not continued in the main checkout, start a new job instead"
}

func (e *ContinuationWorktreeError) Unwrap() error { return ErrContinuationWorktreeGone }

// worktreeFromID is the WorktreeFrom a continuation of src carries: src's own id when
// src is a worktree job (the branch is kept after a cleanup), else "".
func worktreeFromID(src JobResult) string {
	if src.WorktreeBranch == "" {
		return ""
	}
	return src.ID
}

// worktreeUsable: path is a directory, git sees it as its own checkout toplevel (not a
// leftover directory that resolves to the main repo), and the branch ref still exists.
// HEAD need not be on the branch (a rebase / cherry-pick stop is detached).
func worktreeUsable(ctx context.Context, path, branch string) bool {
	if path == "" || branch == "" {
		return false
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return false
	}
	top, err := gitTopLevel(ctx, path)
	if err != nil || !samePath(util.RealPath(top), util.RealPath(path)) {
		return false
	}
	_, err = gitOut(ctx, path, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// continuationWorktree is the managed worktree a continuation of srcID keeps. A source
// that is not a worktree job gives (nil, nil): an ordinary job. A worktree job whose
// worktree is unusable, or whose resolved cwd (workDir) is not inside it, is refused.
func (s *Service) continuationWorktree(srcID, workDir string) (*worktreeRef, error) {
	src, ok := s.Get(srcID)
	if !ok || src.WorktreeBranch == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
	defer cancel()
	if !worktreeUsable(ctx, src.WorktreePath, src.WorktreeBranch) {
		return nil, &ContinuationWorktreeError{Reason: reasonWorktreeGone, Path: src.WorktreePath}
	}
	rel, err := filepath.Rel(util.RealPath(src.WorktreePath), util.RealPath(workDir))
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, &ContinuationWorktreeError{Reason: reasonWorktreeOutsideProject, Path: src.WorktreePath}
	}
	return &worktreeRef{Path: src.WorktreePath, Branch: src.WorktreeBranch, BaseSHA: src.WorktreeBaseSHA,
		Links: src.WorktreeLinks}, nil
}

// recheckContinuationWorktree runs just before the job starts: the worktree may have
// been removed while the job sat in a queue. A temp checkout and a forwarded job are
// not judged here.
func (s *Service) recheckContinuationWorktree(entry *jobEntry, req runner.Request) error {
	entry.mu.Lock()
	wt := entry.wt
	entry.mu.Unlock()
	if wt == nil || wt.Temp || req.Forward != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
	defer cancel()
	if !worktreeUsable(ctx, wt.Path, wt.Branch) {
		return &ContinuationWorktreeError{Reason: reasonWorktreeGone, Path: wt.Path}
	}
	return nil
}

// beforeMergeCleanup is a test seam between the merge-time and the cleanup-time check;
// nil in production.
var beforeMergeCleanup func()

// guardWorktreeIdle refuses a merge (and, at cleanup time, the removal) while a live job
// still works in the worktree at path; same rule as `worktree rm`.
func (s *Service) guardWorktreeIdle(path string, cleanup bool) error {
	if cleanup && beforeMergeCleanup != nil {
		beforeMergeCleanup()
	}
	if users := s.worktreeUsers(path); len(users) > 0 {
		return fmt.Errorf(worktreeInUseFormat, ErrWorktreeInUse, path, strings.Join(users, ", "))
	}
	return nil
}

// cleanupWarning is the merge warning for a failed --cleanup: a checkout kept because a
// job is using it is "skipped", anything else "failed".
func cleanupWarning(err error) string {
	if errors.Is(err, ErrWorktreeInUse) {
		return "cleanup skipped: " + err.Error()
	}
	return "cleanup failed: " + err.Error()
}

// recordContinuationSkipped writes job.continuation_skipped on the source job when err
// is a worktree-gone refusal of an automatic continuation; any other error is not its
// business.
func (s *Service) recordContinuationSkipped(srcID, entry string, err error) {
	if !errors.Is(err, ErrContinuationWorktreeGone) {
		return
	}
	var cwe *ContinuationWorktreeError
	reason, path := reasonWorktreeGone, ""
	if errors.As(err, &cwe) {
		reason, path = cwe.Reason, cwe.Path
	}
	slog.Warn("continuation skipped: worktree gone", "job_id", srcID, "entry", entry, "reason", reason, "path", path)
	s.recordEvent(srcID, EventJobContinuationSkipped, map[string]any{
		"entry": entry, "reason": reason, "worktree_path": path,
	})
}

// cancelRetryWorktreeGone ends a pending retry whose Submit was refused because the
// worktree is gone: the retry row is cancelled (a retry cannot bring the worktree
// back), the failure stays final and the source job records why. It reports whether
// err was that refusal.
func (s *Service) cancelRetryWorktreeGone(rec jobstore.RetryRecord, err error) bool {
	if !errors.Is(err, ErrContinuationWorktreeGone) {
		return false
	}
	if cerr := s.meta.CancelRetry(rec.ID); cerr != nil {
		slog.Warn("retry sweep: cancel worktree-gone retry", "retry_id", rec.ID, "err", cerr)
	}
	s.recordEvent(rec.SourceJobID, EventJobRetryCancelled,
		map[string]any{"retry_id": rec.ID, "reason": err.Error()})
	s.deliverRetryCancelledReceipts(rec.SourceJobID)
	s.recordContinuationSkipped(rec.SourceJobID, "retry", err)
	return true
}
