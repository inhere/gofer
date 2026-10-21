package job

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `ref:` 临时检出（design 一·3）的测试：临时仓库、不含真实 server 与 PATH 上的 gofer。

// TestResolveCommitRef covers the three spellings a template may name — a branch, a tag
// and an abbreviated sha — plus the refusal of something that is not a commit.
func TestResolveCommitRef(t *testing.T) {
	repo, head := gitRepo(t)
	payload := payloadCommit(t, repo, "feat", "f.txt", "payload\n")
	gitOutIn(t, repo, "tag", "v1.0.0", head)

	for _, ref := range []string{"main", "v1.0.0", head[:7], head} {
		got, err := ResolveCommitRef(t.Context(), repo, ref)
		if err != nil {
			t.Fatalf("ResolveCommitRef(%q) = %v", ref, err)
		}
		if got != head {
			t.Fatalf("ResolveCommitRef(%q) = %s, want %s", ref, got, head)
		}
	}
	// A branch resolves to ITS tip, not to the checkout's HEAD.
	if got, err := ResolveCommitRef(t.Context(), repo, "feat"); err != nil || got != payload {
		t.Fatalf("ResolveCommitRef(feat) = %s, %v; want %s", got, err, payload)
	}
	if _, err := ResolveCommitRef(t.Context(), repo, "no-such-ref"); err == nil {
		t.Fatal("ResolveCommitRef(no-such-ref) = nil error, want a refusal")
	} else if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

// TestFetchCommitFetchesFromRemote proves a machine whose clone lacks the commit (the
// worker case) gets it from the default remote before the temporary checkout is created.
func TestFetchCommitFetchesFromRemote(t *testing.T) {
	remote := t.TempDir()
	gitOutIn(t, remote, "init", "-q", "--bare", "-b", "main")

	origin, _ := gitRepo(t)
	gitOutIn(t, origin, "remote", "add", "origin", remote)
	gitOutIn(t, origin, "push", "-q", "origin", "main")

	// The clone is made BEFORE the commit exists on the remote, so it starts out without
	// it — exactly the worker that has never seen the hub's commit.
	clone := t.TempDir()
	gitOutIn(t, clone, "clone", "-q", remote, ".")

	sha := payloadCommit(t, origin, "work", "w.txt", "work\n")
	gitOutIn(t, origin, "push", "-q", "origin", "work")

	if commitPresent(t.Context(), clone, sha) {
		t.Fatal("fixture: the clone must not have the commit before the fetch")
	}
	if err := fetchCommit(t.Context(), clone, sha); err != nil {
		t.Fatalf("fetchCommit = %v, want nil", err)
	}
	if !commitPresent(t.Context(), clone, sha) {
		t.Fatal("fetchCommit did not bring the commit into the clone")
	}
}

// TestFetchCommitReportsUnpushedCommit: when the remote does not have the commit either,
// the job fails with the actionable reason (push it, or check on the server's own runner)
// instead of running some other tree.
func TestFetchCommitReportsUnpushedCommit(t *testing.T) {
	remote := t.TempDir()
	gitOutIn(t, remote, "init", "-q", "--bare", "-b", "main")
	origin, _ := gitRepo(t)
	gitOutIn(t, origin, "remote", "add", "origin", remote)
	gitOutIn(t, origin, "push", "-q", "origin", "main")
	// A commit that exists only in the origin's local branch: never pushed.
	sha := payloadCommit(t, origin, "private", "p.txt", "private\n")

	clone := t.TempDir()
	gitOutIn(t, clone, "clone", "-q", remote, ".")
	err := fetchCommit(t.Context(), clone, sha)
	if err == nil {
		t.Fatal("fetchCommit of an unpushed commit = nil, want a refusal")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "先推送该提交") {
		t.Fatalf("error = %q, want it to say the commit must be pushed first", err)
	}
	if commitPresent(t.Context(), clone, sha) {
		t.Fatal("an unpushed commit must not appear in the clone")
	}
}

// TestRefJobRunsInTempCheckoutAndCleansUp is the end-to-end of `ref:`: the job runs in a
// detached checkout of the requested commit (not the main checkout, not HEAD), and that
// checkout is GONE once the job is done — this mode has no deliverable to keep.
func TestRefJobRunsInTempCheckoutAndCleansUp(t *testing.T) {
	repo, _ := gitRepo(t)
	// A commit that is NOT the checkout's HEAD: a job that ignores ref would report HEAD.
	target := payloadCommit(t, repo, "candidate", "c.txt", "candidate\n")

	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "rev-parse", "HEAD"}, Cwd: ".", TimeoutSec: 60, Ref: target,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	if got := strings.TrimSpace(repoLog(t, state, final.ID)); got != target {
		t.Fatalf("job ran on %q, want the ref commit %q", got, target)
	}
	if final.WorktreePath != "" {
		t.Fatalf("terminal row still advertises worktree %q, want none for a temp checkout", final.WorktreePath)
	}
	if final.WorktreeBaseSHA != target {
		t.Fatalf("worktree_base_sha = %q, want the resolved ref %q", final.WorktreeBaseSHA, target)
	}
	assertTempCheckoutGone(t, repo)
}

// TestRefJobFailureStillCleansUp: a failing job leaves no checkout behind either.
func TestRefJobFailureStillCleansUp(t *testing.T) {
	repo, head := gitRepo(t)
	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "rev-parse", "--verify", "refs/heads/no-such-branch"},
		Cwd: ".", TimeoutSec: 60, Ref: head,
	})
	if final.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	assertTempCheckoutGone(t, repo)
}

// TestRefAndWorktreeAreMutuallyExclusive: one is a throwaway detached checkout, the other
// a branch meant to be delivered — a request for both is refused before anything is made.
func TestRefAndWorktreeAreMutuallyExclusive(t *testing.T) {
	repo, head := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	_, err := s.Submit(JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"true"}, Cwd: ".", TimeoutSec: 30, Ref: head, Worktree: true,
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Submit(ref+worktree) = %v, want a mutual-exclusion refusal", err)
	}
}

// assertTempCheckoutGone checks that no temporary checkout of the repository survived: the
// <top>/tmp/gofer/wt tree is empty (the fixture's tmp/ is ignored, so this never trips on
// unrelated files) and git no longer lists a worktree there.
func assertTempCheckoutGone(t *testing.T, repo string) {
	t.Helper()
	wtRoot := filepath.Join(repo, filepath.FromSlash(worktreeSubdir))
	entries, err := os.ReadDir(wtRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", wtRoot, err)
	}
	if len(entries) > 0 {
		t.Fatalf("temporary checkouts left behind: %v", entries)
	}
	if out := gitOutIn(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, filepath.ToSlash(wtRoot)) {
		t.Fatalf("git still lists a worktree under %s:\n%s", wtRoot, out)
	}
}
