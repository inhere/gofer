package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// TestSweepStaleTempCheckouts pins the startup cleanup of leftover `ref:` checkouts
// (design 2026-10-21 §一·3): a directory no live job owns is removed with the finish
// path's cleanup (unlink, `worktree remove --force`, `worktree prune`), while a checkout
// whose job row is still non-terminal is kept. A finished row's checkout goes.
func TestSweepStaleTempCheckouts(t *testing.T) {
	repo, head := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	root := filepath.Join(repo, filepath.FromSlash(worktreeSubdir))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// One real (registered) temporary checkout and one plain stale directory, both named
	// after gofer-generated job ids whose rows are gone (pruned): leftovers of ours.
	gitOutIn(t, repo, "worktree", "add", "--detach", filepath.Join(root, "job-20261022-100000-0000aaaa"), head)
	if err := os.MkdirAll(filepath.Join(root, "job-20261022-100000-0000bbbb"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A live job owns this one: its row is still running, so it must survive.
	live := filepath.Join(root, "job-live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "job-live", Status: StatusRunning, Cwd: ".", ResultDir: filepath.Join(t.TempDir(), "live"),
	}); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}

	if n := s.SweepStaleTempCheckouts(t.Context()); n != 2 {
		t.Fatalf("swept %d checkout(s), want 2 (the stale ones only)", n)
	}
	if isDir(filepath.Join(root, "job-20261022-100000-0000aaaa")) || isDir(filepath.Join(root, "job-20261022-100000-0000bbbb")) {
		t.Fatal("a stale checkout survived the sweep")
	}
	if !isDir(live) {
		t.Fatal("the sweep removed a running job's checkout")
	}
	// The worked-up repo must not still list the removed checkout.
	if out := gitOutIn(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, filepath.Join(root, "job-20261022-100000-0000aaaa")) {
		t.Fatalf("worktree list still names the swept checkout:\n%s", out)
	}
	// Once the job's row is terminal, its checkout is stale too.
	done, _, err := s.meta.GetJob("job-live")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	done.Status = StatusDone
	if err := s.meta.UpsertJob(done); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if n := s.SweepStaleTempCheckouts(t.Context()); n != 1 {
		t.Fatalf("swept %d checkout(s) after the job ended, want 1", n)
	}
	if isDir(live) {
		t.Fatal("a finished job's checkout survived the sweep")
	}
}

// TestSweepStaleTempCheckoutsUsesProjectWorktreeDir is the gofer-bgkk regression: the
// sweep used to look at the DEFAULT `tmp/gofer/wt` only, so a project that puts its
// checkouts in `.worktrees/job-{job_id}` (gofer's own config) never had a leftover
// reclaimed — nine of them survived several restarts. The scan must follow the project's
// worktree.dir template, read the job id back out of the dir name, and leave a branch
// checkout alone (that is a `--worktree` deliverable, not a throwaway `ref:` checkout).
func TestSweepStaleTempCheckoutsUsesProjectWorktreeDir(t *testing.T) {
	repo, head := gitRepo(t)
	s := newWorktreeServiceWithProject(t, repo, t.TempDir(), func(p *config.ProjectConfig) {
		p.Worktree = &config.ProjectWorktree{Dir: ".worktrees/job-{job_id}"}
	})
	root := filepath.Join(repo, ".worktrees")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A leftover of a job that already ended: exactly what a failed removal leaves.
	leftover := filepath.Join(root, "job-finished1")
	gitOutIn(t, repo, "worktree", "add", "--detach", leftover, head)
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "finished1", Status: StatusFailed, Cwd: ".", ResultDir: filepath.Join(t.TempDir(), "f1"),
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	// A checkout whose job still runs: kept.
	running := filepath.Join(root, "job-running1")
	gitOutIn(t, repo, "worktree", "add", "--detach", running, head)
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "running1", Status: StatusRunning, Cwd: ".", ResultDir: filepath.Join(t.TempDir(), "r1"),
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	// A `--worktree` deliverable (HEAD on a branch): never swept, even with a dead job.
	delivered := filepath.Join(root, "job-delivered1")
	gitOutIn(t, repo, "worktree", "add", "-b", "gofer/delivered1", delivered, head)
	// An unrelated worktree name (a hand-made one) is not a job checkout at all.
	manual := filepath.Join(root, "manual-check")
	gitOutIn(t, repo, "worktree", "add", "--detach", manual, head)

	if n := s.SweepStaleTempCheckouts(t.Context()); n != 1 {
		t.Fatalf("swept %d checkout(s), want 1 (the finished job's leftover)", n)
	}
	if isDir(leftover) {
		t.Fatalf("leftover checkout %s survived the sweep", leftover)
	}
	if !isDir(running) || !isDir(delivered) || !isDir(manual) {
		t.Fatalf("sweep removed a checkout it does not own (running=%v delivered=%v manual=%v)",
			isDir(running), isDir(delivered), isDir(manual))
	}
	if out := gitOutIn(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, leftover) {
		t.Fatalf("git still lists the swept checkout:\n%s", out)
	}
	// The completion of a failed removal is recorded on the job's timeline.
	events, err := s.meta.ListJobEvents("finished1", 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Type == EventJobTempCheckoutSwept {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s event recorded for the swept checkout (events: %+v)", EventJobTempCheckoutSwept, events)
	}
}

// TestSweepStaleTempCheckoutsNestedRepo is the gofer-l0zi regression: a `ref:` / `--worktree`
// checkout is created at the job's NEAREST git toplevel, so in a project that carries other
// repositories (inhere-tools/gofer inside my-tools-dev) the leftovers sit in the NESTED
// repository's worktree dir — nine of them survived the gofer-bgkk sweep, which only ever
// listed the project root's. The repositories to scan are replayed from what the project's
// job rows recorded as their working directory, and both shapes pin their own resolution:
// the cwd of a `ref:` job IS its temporary checkout (a linked worktree, which must resolve
// to the repository it was cut from) and a plain cwd is any directory inside that checkout.
// The project-root scan keeps working, and a nested checkout a live job owns is never touched.
func TestSweepStaleTempCheckoutsNestedRepo(t *testing.T) {
	outer, outerHead := gitRepo(t)
	// nested returns a git repository inside the project checkout, with one commit.
	nested := func(name string) (string, string) {
		t.Helper()
		dir := filepath.Join(outer, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gitOutIn(t, dir, "init", "-q", "-b", "main")
		gitOutIn(t, dir, "config", "user.email", "gofer-test@example.com")
		gitOutIn(t, dir, "config", "user.name", "gofer-test")
		writeRepoFile(t, dir, name+".txt", "nested fixture\n")
		gitOutIn(t, dir, "add", ".")
		gitOutIn(t, dir, "commit", "-q", "-m", "nested init")
		return dir, gitOutIn(t, dir, "rev-parse", "HEAD")
	}
	// repoA is reached ONLY through `ref:` job cwds (its own checkouts), repoB only through a
	// plain cwd: a sweep that resolves neither, or only one of them, leaves a leftover behind.
	repoA, headA := nested("repoA")
	repoB, headB := nested("repoB")

	s := newWorktreeServiceWithProject(t, outer, t.TempDir(), func(p *config.ProjectConfig) {
		p.Worktree = &config.ProjectWorktree{Dir: ".worktrees/job-{job_id}"}
	})
	upsert := func(id, status, cwd string) {
		t.Helper()
		if err := s.meta.UpsertJob(jobstore.JobRecord{
			ID: id, ProjectKey: "repo", Status: status, Cwd: cwd, ResultDir: filepath.Join(t.TempDir(), id),
		}); err != nil {
			t.Fatalf("UpsertJob(%s): %v", id, err)
		}
	}
	checkout := func(repo, id, head string) string {
		t.Helper()
		path := filepath.Join(repo, ".worktrees", "job-"+id)
		gitOutIn(t, repo, "worktree", "add", "--detach", path, head)
		return path
	}
	// A leftover of a dead `ref:` job, recorded through its own checkout — the shape of the
	// nine gofer-l0zi leftovers.
	deadA := checkout(repoA, "nesteda", headA)
	upsert("nesteda", StatusFailed, deadA)
	// A checkout of the same repository a live job still runs in: kept.
	liveA := checkout(repoA, "nestedlive", headA)
	upsert("nestedlive", StatusRunning, liveA)
	// A nested leftover whose job recorded a plain cwd inside its repository.
	deadB := checkout(repoB, "nestedb", headB)
	upsert("nestedb", StatusFailed, repoB)
	// The project-root case gofer-bgkk fixed: still swept.
	rootLeftover := filepath.Join(outer, ".worktrees", "job-root1")
	gitOutIn(t, outer, "worktree", "add", "--detach", rootLeftover, outerHead)
	upsert("root1", StatusFailed, outer)

	if n := s.SweepStaleTempCheckouts(t.Context()); n != 3 {
		t.Fatalf("swept %d checkout(s), want 3 (two nested leftovers + the project-root one)", n)
	}
	for _, path := range []string{deadA, deadB, rootLeftover} {
		if isDir(path) {
			t.Fatalf("stale checkout %s survived the sweep", path)
		}
	}
	if !isDir(liveA) {
		t.Fatal("the sweep removed a nested checkout a running job owns")
	}
	for _, repo := range []string{outer, repoA, repoB} {
		out := gitOutIn(t, repo, "worktree", "list", "--porcelain")
		for _, gone := range []string{deadA, deadB, rootLeftover} {
			if strings.Contains(out, gone) {
				t.Fatalf("git in %s still lists the swept checkout %s:\n%s", repo, gone, out)
			}
		}
	}
}

// TestSweepStaleTempCheckoutsDoesNotFollowLinks: the checkout carries a dependency link
// (the project's node_modules equivalent). Removing the checkout must unlink it, not walk
// into its target — the external directory and its contents survive.
func TestSweepStaleTempCheckoutsDoesNotFollowLinks(t *testing.T) {
	repo, head := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	root := filepath.Join(repo, filepath.FromSlash(worktreeSubdir))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeRepoFile(t, outside, "precious.txt", "keep me\n")

	path := filepath.Join(root, "job-linked")
	gitOutIn(t, repo, "worktree", "add", "--detach", path, head)
	// The checkout belongs to a finished job (the sweep only touches a job of ours).
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "job-linked", Status: StatusFailed, Cwd: ".", ResultDir: filepath.Join(t.TempDir(), "l1"),
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if err := createDirLink(outside, filepath.Join(path, "node_modules")); err != nil {
		t.Fatalf("create dependency link: %v", err)
	}

	if n := s.SweepStaleTempCheckouts(t.Context()); n != 1 {
		t.Fatalf("swept %d checkout(s), want 1", n)
	}
	if isDir(path) {
		t.Fatal("the stale linked checkout survived the sweep")
	}
	if body, err := os.ReadFile(filepath.Join(outside, "precious.txt")); err != nil || strings.TrimSpace(string(body)) != "keep me" {
		t.Fatalf("the link target was damaged (body=%q err=%v)", body, err)
	}
}

// TestCleanupTempWorktreeRetriesLockedDirectory: a directory that git refuses once (a
// Windows process still holding it) is removed by the retry instead of being left for the
// next sweep. Simulated by a checkout dir git never registered (a half-created one) inside
// a fresh temporary ref — the removal must still complete, synchronously.
func TestCleanupTempWorktreeRetriesLockedDirectory(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	root := filepath.Join(repo, filepath.FromSlash(worktreeSubdir))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "job-halfmade")
	writeRepoFile(t, path, "junk.txt", "half created\n")

	entry := &jobEntry{done: make(chan struct{}), wt: &worktreeRef{Path: path, Top: repo, Temp: true}}
	entry.result.ID = "halfmade"
	s.cleanupTempWorktree(entry)
	wait.Until(t, 5*time.Second, "the half-created checkout is removed", func() bool { return !isDir(path) })

	if entry.wt != nil || entry.result.WorktreePath != "" {
		t.Fatalf("the entry still advertises a checkout: wt=%v path=%q", entry.wt, entry.result.WorktreePath)
	}
}

// TestSweepStaleTempCheckoutsKeepsForeignNames: with a `{name}` template every directory
// name matches, so the sweep must not read an arbitrary name as a dead job. A release
// build's detached `.worktrees/rel-v0.191.1` checkout was deleted mid-build that way
// (2026-10-23). Only a finished job's row, or — row pruned — a gofer-generated job id,
// makes a detached checkout sweepable.
func TestSweepStaleTempCheckoutsKeepsForeignNames(t *testing.T) {
	repo, head := gitRepo(t)
	s := newWorktreeServiceWithProject(t, repo, t.TempDir(), func(p *config.ProjectConfig) {
		p.Worktree = &config.ProjectWorktree{Dir: ".worktrees/{name}"}
	})
	root := filepath.Join(repo, ".worktrees")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(root, "rel-v0.191.1")
	gitOutIn(t, repo, "worktree", "add", "--detach", release, head)
	manual := filepath.Join(root, "manual-check")
	gitOutIn(t, repo, "worktree", "add", "--detach", manual, head)
	// A gofer job id whose row was pruned: still a leftover of ours.
	pruned := filepath.Join(root, "job-20261022-164100-0123abcd")
	gitOutIn(t, repo, "worktree", "add", "--detach", pruned, head)

	if n := s.SweepStaleTempCheckouts(t.Context()); n != 1 {
		t.Fatalf("swept %d checkout(s), want 1 (the pruned job's leftover)", n)
	}
	if !isDir(release) || !isDir(manual) {
		t.Fatalf("sweep removed a checkout gofer does not own (release=%v manual=%v)", isDir(release), isDir(manual))
	}
	if isDir(pruned) {
		t.Fatalf("leftover of a pruned job %s survived the sweep", pruned)
	}
}
