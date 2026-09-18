package job

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
)

// TestCommitsCapturedFromBaseSHA: a job records the commit it started from on the
// executing machine and, at its terminal, the commits it produced between that
// base and HEAD — newest first — so `job show` / the web and the todo note can
// name what a job actually delivered. The capture runs whether or not the diff
// capture is enabled, and a cwd that is not a repository leaves it empty.
func TestCommitsCapturedFromBaseSHA(t *testing.T) {
	repo, base := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())

	// Two payload commits on side branches, applied by the job itself: one argv
	// (`git cherry-pick <sha1> <sha2>`) produces two real commits in the repo.
	sha1 := payloadCommit(t, repo, "payload-1", "one.txt", "first\n")
	sha2 := payloadCommit(t, repo, "payload-2", "two.txt", "second\n")

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "cherry-pick", sha1, sha2}, Cwd: ".", TimeoutSec: 60,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.BaseSHA != base {
		t.Fatalf("base_sha = %q, want the checkout's HEAD at job start %q", final.BaseSHA, base)
	}
	if len(final.Commits) != 2 {
		t.Fatalf("commits = %+v, want the 2 applied commits", final.Commits)
	}
	if final.Commits[0].Subject != "payload two.txt" || final.Commits[1].Subject != "payload one.txt" {
		t.Fatalf("commits must be newest first: %+v", final.Commits)
	}
	if final.Commits[0].SHA != gitOutIn(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("the first entry must be HEAD: %+v", final.Commits[0])
	}
	// Each sha is a real abbreviated commit id (what the note copies).
	for _, c := range final.Commits {
		if len(c.SHA) < 7 || c.SHA[0] == ' ' {
			t.Fatalf("commit sha looks wrong: %+v", c)
		}
	}

	// A job in a directory that is not a repository attempts the capture and
	// leaves it empty instead of failing or inventing a value.
	plain := newTestService(t, t.TempDir())
	noRepo := submitAndWait(t, plain, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if noRepo.BaseSHA != "" || len(noRepo.Commits) != 0 {
		t.Fatalf("a non-git cwd must leave base_sha/commits empty: %+v", noRepo)
	}
}

// TestApplyOutcomeCarriesCommits: a remote execution machine's捕获 (SUP-01 C)
// lands on the host job row through the Outcome the runner回传 — the other half of
// the worker path the commits are captured on.
func TestApplyOutcomeCarriesCommits(t *testing.T) {
	s := newTestService(t, t.TempDir())
	entry := &jobEntry{result: JobResult{ID: "job-remote", ProjectKey: "self"}, done: make(chan struct{})}

	s.applyOutcome(entry, &runner.Outcome{
		Source:  "worker:w1",
		BaseSHA: "base-remote",
		Commits: []runner.Commit{{SHA: "c1", Subject: "remote one"}, {SHA: "c2", Subject: "remote two"}},
	})

	if entry.result.BaseSHA != "base-remote" {
		t.Fatalf("base_sha = %q, want base-remote", entry.result.BaseSHA)
	}
	if len(entry.result.Commits) != 2 || entry.result.Commits[0].Subject != "remote one" {
		t.Fatalf("commits = %+v, want the remote list in order", entry.result.Commits)
	}

	// And it survives the projection into the store (what a later `job show` reads).
	rec := toRecord(entry.result)
	if rec.BaseSHA != "base-remote" || rec.CommitsJSON == "" {
		t.Fatalf("record lost the captured commits: base_sha=%q commits_json=%q", rec.BaseSHA, rec.CommitsJSON)
	}
	back := fromRecord(jobstore.JobRecord{ID: "job-remote", BaseSHA: rec.BaseSHA, CommitsJSON: rec.CommitsJSON})
	if len(back.Commits) != 2 || back.Commits[1].SHA != "c2" {
		t.Fatalf("fromRecord lost the commits: %+v", back.Commits)
	}
}
