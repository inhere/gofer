package job

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/util"
)

// WT-01 受管 worktree 的测试。全部用 t.TempDir() 里 `git init` 出的**临时仓库**：绝不
// 碰真实仓库，也不读真实配置（G：测试一律 t.TempDir()）。仓库里预置 .gitignore 忽略
// tmp/，与真实项目同构——worktree 落在 <top>/tmp/gofer/wt/<id>，若 tmp/ 没被忽略就会
// 在主 checkout 的 `git status` 里冒出来，"主 checkout 无改动"的断言正依赖这一点。

// gitOutIn 在 dir 里跑一条 git 命令并返回 stdout；失败即测试失败（测试夹具，不降级）。
func gitOutIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitRepo 建一个临时 git 仓库（主干 main，含 .gitignore 忽略 tmp/ 与一个初始提交），
// 返回仓库路径与初始提交 sha。user.name/email 就地配置，让 job 里的 `git commit`
// 在没有任何全局 git 身份的机器（CI 容器）上也能工作。
func gitRepo(t *testing.T) (repo, head string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("worktree tests require git on PATH: %v", err)
	}
	repo = t.TempDir()
	gitOutIn(t, repo, "init", "-q", "-b", "main")
	gitOutIn(t, repo, "config", "user.email", "gofer-test@example.com")
	gitOutIn(t, repo, "config", "user.name", "gofer-test")
	gitOutIn(t, repo, "config", "commit.gpgsign", "false")
	gitOutIn(t, repo, "config", "core.autocrlf", "false")
	writeRepoFile(t, repo, ".gitignore", "tmp/\n")
	writeRepoFile(t, repo, "README.md", "worktree fixture\n")
	gitOutIn(t, repo, "add", ".")
	gitOutIn(t, repo, "commit", "-q", "-m", "init")
	return repo, gitOutIn(t, repo, "rev-parse", "HEAD")
}

// payloadCommit 在 repo 上从当前主干切一个临时分支，提交一个真实文件改动，再切回主干，
// 返回该提交的 sha（HEAD 不变）。--worktree job 用它做 `git cherry-pick`：一条 argv 就能
// "改文件 + 生成提交"，从而让分支上出现真实的已提交改动（跨平台，不依赖 shell）。
func payloadCommit(t *testing.T, repo, branch, file, content string) string {
	t.Helper()
	gitOutIn(t, repo, "checkout", "-q", "-b", branch)
	writeRepoFile(t, repo, file, content)
	gitOutIn(t, repo, "add", "--", file)
	gitOutIn(t, repo, "commit", "-q", "-m", "payload "+file)
	sha := gitOutIn(t, repo, "rev-parse", "HEAD")
	gitOutIn(t, repo, "checkout", "-q", "main")
	return sha
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// newWorktreeService builds a Service whose single "repo" project points at a git
// checkout, with the result-dir storage root OUTSIDE that checkout (a job's result
// dir must never land in the repo it diffs).
func newWorktreeService(t *testing.T, repo, state string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: state},
		Projects: map[string]config.ProjectConfig{
			"repo": {
				HostPath:       repo,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(state, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
}

// repoLog reads a job's stdout log (the "repo" project's FileStore under state).
func repoLog(t *testing.T, state, jobID string) string {
	t.Helper()
	out, err := store.NewFileStore(filepath.Join(state, "repo")).ReadLogTail(jobID, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("read stdout log of %s: %v", jobID, err)
	}
	return string(out)
}

// TestWorktreeRejectsNonGitCwd proves --worktree refuses to run where there is no
// checkout to fork from, instead of silently executing in the shared directory:
// a non-git cwd is rejected with "worktree requires a git checkout", and a machine
// with no git on PATH with its own explicit refusal.
func TestWorktreeRejectsNonGitCwd(t *testing.T) {
	state := t.TempDir()
	s := newWorktreeService(t, t.TempDir(), state) // project root: NOT a git repo
	req := JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, Worktree: true,
	}
	_, err := s.Submit(req)
	if err == nil {
		t.Fatal("Submit with --worktree in a non-git cwd must fail")
	}
	if !strings.Contains(err.Error(), "worktree requires a git checkout") {
		t.Fatalf("error = %q, want it to mention %q", err, "worktree requires a git checkout")
	}

	// git itself missing is a different failure and must say so (not be reported as
	// "not a checkout", which would send the operator looking for a .git dir).
	t.Setenv("PATH", "")
	_, err = s.Submit(req)
	if err == nil {
		t.Fatal("Submit with --worktree and no git on PATH must fail")
	}
	if !strings.Contains(err.Error(), "worktree requires git on PATH") {
		t.Fatalf("error = %q, want it to mention git missing from PATH", err)
	}
}

// TestWorktreeNestedRepoUsesNearestToplevel proves the repository is located from
// the job's cwd, so a nested checkout (the gofer-in-a-monorepo layout) forks its
// OWN toplevel instead of the outer repository.
func TestWorktreeNestedRepoUsesNearestToplevel(t *testing.T) {
	outer, _ := gitRepo(t)
	nested := filepath.Join(outer, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOutIn(t, nested, "init", "-q", "-b", "main")
	gitOutIn(t, nested, "config", "user.email", "gofer-test@example.com")
	gitOutIn(t, nested, "config", "user.name", "gofer-test")
	writeRepoFile(t, nested, "n.txt", "nested\n")
	gitOutIn(t, nested, "add", ".")
	gitOutIn(t, nested, "commit", "-q", "-m", "nested init")

	state := t.TempDir()
	s := newWorktreeService(t, outer, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "rev-parse", "--show-toplevel"}, Cwd: "nested",
		TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	wantPrefix := util.RealPath(filepath.Join(nested, filepath.FromSlash(worktreeSubdir)))
	if !strings.HasPrefix(final.WorktreePath, wantPrefix+string(filepath.Separator)) {
		t.Fatalf("worktree path = %q, want it under the NESTED toplevel %q", final.WorktreePath, wantPrefix)
	}
	if got := filepath.ToSlash(strings.TrimSpace(repoLog(t, state, final.ID))); got != filepath.ToSlash(final.WorktreePath) {
		t.Fatalf("job ran in %q, want the worktree %q", got, final.WorktreePath)
	}
}

// TestWorktreeSymlinkedProjectRoot covers the same mapping when the project root is
// reached through a SYMLINK: `git rev-parse --show-toplevel` answers with the
// resolved path while the project keeps the operator's spelling, so the two names
// differ without being different directories. macOS makes this the default case
// (t.TempDir() hands out /var/... and git answers /private/var/...), which is why
// the whole worktree suite failed there while passing on Linux/Windows.
func TestWorktreeSymlinkedProjectRoot(t *testing.T) {
	real, _ := gitRepo(t)
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}

	state := t.TempDir()
	s := newWorktreeService(t, link, state) // project root: the symlink spelling
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "rev-parse", "--show-toplevel"}, Cwd: ".", TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	resolved := util.RealPath(real)
	wantPrefix := filepath.Join(resolved, filepath.FromSlash(worktreeSubdir))
	if !strings.HasPrefix(final.WorktreePath, wantPrefix+string(filepath.Separator)) {
		t.Fatalf("worktree path = %q, want it under the resolved toplevel %q", final.WorktreePath, wantPrefix)
	}
	if got := filepath.ToSlash(strings.TrimSpace(repoLog(t, state, final.ID))); got != filepath.ToSlash(final.WorktreePath) {
		t.Fatalf("job ran in %q, want the worktree %q", got, final.WorktreePath)
	}
}

// TestWorktreeMapsCwdSubpath proves --cwd is mapped into the worktree by the same
// relative sub-path: the job's process really starts in <worktree>/sub, not in the
// worktree root and not in the parent checkout.
func TestWorktreeMapsCwdSubpath(t *testing.T) {
	repo, _ := gitRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, repo, "sub/tracked.txt", "tracked\n")
	gitOutIn(t, repo, "add", ".")
	gitOutIn(t, repo, "commit", "-q", "-m", "add sub")

	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "rev-parse", "--show-toplevel", "--show-prefix"}, Cwd: "sub",
		TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}

	// The worktree path is built from `git rev-parse --show-toplevel`, i.e. the
	// symlink-RESOLVED spelling (macOS /var -> /private/var), so resolve the
	// expectation built from the temp dir the same way.
	wantWt := util.RealPath(filepath.Join(repo, filepath.FromSlash(worktreeSubdir), final.ID))
	if filepath.Clean(final.WorktreePath) != filepath.Clean(wantWt) {
		t.Fatalf("worktree path = %q, want %q", final.WorktreePath, wantWt)
	}
	wantCwd := filepath.Join(wantWt, "sub")
	if filepath.Clean(final.Cwd) != filepath.Clean(wantCwd) {
		t.Fatalf("job cwd = %q, want the mapped subpath %q", final.Cwd, wantCwd)
	}
	// --show-toplevel = the worktree; --show-prefix = "sub/" proves the process cwd.
	lines := strings.Fields(repoLog(t, state, final.ID))
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want toplevel + prefix", lines)
	}
	if filepath.ToSlash(lines[0]) != filepath.ToSlash(wantWt) {
		t.Fatalf("process toplevel = %q, want %q", lines[0], wantWt)
	}
	if lines[1] != "sub/" {
		t.Fatalf("process prefix = %q, want %q", lines[1], "sub/")
	}
}

// TestWorktreeParallelJobsIsolated is the acceptance case for the whole feature:
// two --worktree jobs commit in the same project at the same time, each lands its
// commit on its OWN branch in its OWN checkout, and the main checkout is left
// untouched (no files, no HEAD move, no stray worktree bookkeeping).
func TestWorktreeParallelJobsIsolated(t *testing.T) {
	repo, head := gitRepo(t)
	shaA := payloadCommit(t, repo, "payload-a", "a.txt", "payload a\n")
	shaB := payloadCommit(t, repo, "payload-b", "b.txt", "payload b\n")
	if gitOutIn(t, repo, "rev-parse", "HEAD") != head {
		t.Fatalf("fixture moved main's HEAD")
	}

	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	submit := func(sha string) JobResult {
		t.Helper()
		res, err := s.Submit(JobRequest{
			ProjectKey: "repo", Agent: "exec", Runner: "local",
			Cmd: []string{"git", "cherry-pick", sha}, Cwd: ".",
			TimeoutSec: 60, Worktree: true,
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		return res
	}
	// Both jobs are launched before either is awaited: they really run concurrently
	// against the same repository (the .git/index.lock contention this feature
	// exists to remove).
	runA, runB := submit(shaA), submit(shaB)
	finalA, okA := s.Wait(runA.ID)
	finalB, okB := s.Wait(runB.ID)
	if !okA || !okB {
		t.Fatal("Wait lost a job")
	}
	for _, f := range []JobResult{finalA, finalB} {
		if f.Status != StatusDone {
			t.Fatalf("job %s status = %s (err=%s)", f.ID, f.Status, f.Error)
		}
		if f.CommitsAhead != 1 {
			t.Fatalf("job %s commits_ahead = %d, want 1", f.ID, f.CommitsAhead)
		}
		if f.WorktreeHeadSHA == "" || f.WorktreePath == "" || f.WorktreeBranch == "" {
			t.Fatalf("job %s recorded an incomplete worktree: %+v", f.ID, f)
		}
	}
	if finalA.WorktreePath == finalB.WorktreePath || finalA.WorktreeBranch == finalB.WorktreeBranch {
		t.Fatalf("the two jobs shared a worktree: %s/%s vs %s/%s",
			finalA.WorktreePath, finalA.WorktreeBranch, finalB.WorktreePath, finalB.WorktreeBranch)
	}
	// Each branch carries exactly its own payload file — the checkouts are isolated.
	if _, err := os.Stat(filepath.Join(finalA.WorktreePath, "a.txt")); err != nil {
		t.Fatalf("job A's worktree is missing a.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(finalA.WorktreePath, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("job A's worktree contains job B's file (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(finalB.WorktreePath, "b.txt")); err != nil {
		t.Fatalf("job B's worktree is missing b.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(finalB.WorktreePath, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("job B's worktree contains job A's file (err=%v)", err)
	}
	// The main checkout did not move and holds no changes: tmp/ (where worktrees
	// live) is ignored, so even the worktree dirs do not show up as untracked.
	if got := gitOutIn(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("main HEAD moved to %s, want %s", got, head)
	}
	if got := gitOutIn(t, repo, "status", "--porcelain"); got != "" {
		t.Fatalf("main checkout is dirty after the jobs: %q", got)
	}
	for _, f := range []JobResult{finalA, finalB} {
		if got := gitOutIn(t, repo, "rev-parse", f.WorktreeBranch); got != f.WorktreeHeadSHA {
			t.Fatalf("branch %s = %s, want the recorded head %s", f.WorktreeBranch, got, f.WorktreeHeadSHA)
		}
	}
}

// TestWorktreeDiffIncludesCommits proves changes.diff for a worktree job covers the
// COMMITTED work (base..HEAD) — the part a plain `git diff` of a clean checkout
// misses — and that the --stat summary lands in the DB.
func TestWorktreeDiffIncludesCommits(t *testing.T) {
	repo, _ := gitRepo(t)
	sha := payloadCommit(t, repo, "payload", "deliverable.txt", "committed deliverable\n")

	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "cherry-pick", sha}, Cwd: ".",
		TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	body, err := os.ReadFile(filepath.Join(final.ResultDir, "changes.diff"))
	if err != nil {
		t.Fatalf("read changes.diff: %v", err)
	}
	if !strings.Contains(string(body), "=== committed (") {
		t.Fatalf("changes.diff has no committed section:\n%s", body)
	}
	if !strings.Contains(string(body), "committed deliverable") {
		t.Fatalf("changes.diff does not contain the committed change:\n%s", body)
	}
	if !strings.Contains(final.DiffSummary, "deliverable.txt") {
		t.Fatalf("diff summary = %q, want it to mention deliverable.txt", final.DiffSummary)
	}
}

// TestWorktreeEnvHelper is not a test of its own: it is the CHILD process
// TestWorktreeEnvExported runs (the test binary re-executed as an exec job) to dump
// the worktree env vars the job was started with. Without the guard env var it is a
// no-op, so a normal `go test ./internal/job` run ignores it.
func TestWorktreeEnvHelper(t *testing.T) {
	if os.Getenv("GOFER_WT_ENV_HELPER") != "1" {
		return
	}
	for _, k := range []string{envWorktree, envWorktreeBranch, envWorktreeBase} {
		fmt.Printf("%s=%s\n", k, os.Getenv(k))
	}
}

// TestWorktreeEnvExported proves the three worktree vars reach the job process:
// GOFER_WORKTREE (checkout), GOFER_WORKTREE_BRANCH (gofer/<job-id>) and
// GOFER_WORKTREE_BASE (the base SHA, not the ref name).
func TestWorktreeEnvExported(t *testing.T) {
	repo, head := gitRepo(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd:        []string{exe, "-test.run=^TestWorktreeEnvHelper$"},
		Env:        map[string]string{"GOFER_WT_ENV_HELPER": "1"},
		Cwd:        ".",
		TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	out := repoLog(t, state, final.ID)
	for _, want := range []string{
		envWorktree + "=" + final.WorktreePath,
		envWorktreeBranch + "=" + final.WorktreeBranch,
		envWorktreeBase + "=" + head,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("job env is missing %q:\n%s", want, out)
		}
	}
	if final.WorktreeBranch != worktreeBranchPrefix+final.ID {
		t.Fatalf("branch = %q, want %q", final.WorktreeBranch, worktreeBranchPrefix+final.ID)
	}
	if final.WorktreeBaseSHA != head {
		t.Fatalf("base sha = %q, want the checkout HEAD %q", final.WorktreeBaseSHA, head)
	}
}

// TestWorktreeRmRefusesDirtyWithoutForce proves `job worktree rm` will not throw
// away uncommitted work: a worktree holding a leftover file is refused (with the
// work still on disk), and only an explicit force removes it — together with the
// branch when asked. A successful removal also stops the job row from advertising
// a checkout that no longer exists.
func TestWorktreeRmRefusesDirtyWithoutForce(t *testing.T) {
	repo, _ := gitRepo(t)
	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "status", "--porcelain"}, Cwd: ".",
		TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	// What an agent job typically leaves behind: an uncommitted file.
	writeRepoFile(t, final.WorktreePath, "leftover.txt", "agent leftover\n")

	st, err := s.RemoveWorktree(final.ID, false, false)
	if !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("rm without --force: err = %v, want ErrWorktreeDirty", err)
	}
	if !st.Dirty {
		t.Fatal("status should report the worktree as dirty")
	}
	if !isDir(final.WorktreePath) {
		t.Fatal("a refused rm must leave the worktree in place")
	}

	if _, err := s.RemoveWorktree(final.ID, true, true); err != nil {
		t.Fatalf("rm --force --delete-branch: %v", err)
	}
	if isDir(final.WorktreePath) {
		t.Fatalf("worktree %s still present after rm --force", final.WorktreePath)
	}
	if out := gitOutIn(t, repo, "branch", "--list", final.WorktreeBranch); out != "" {
		t.Fatalf("branch %s still exists: %q", final.WorktreeBranch, out)
	}
	if got, ok := s.Get(final.ID); !ok || got.WorktreePath != "" {
		t.Fatalf("job row still advertises the removed worktree: %+v (ok=%v)", got.WorktreePath, ok)
	}
}

// TestRetentionRemovesMergedWorktree proves the retention sweep reclaims a worktree
// whose deliverable is safe: the job row is pruned, the branch was merged into the
// base branch, so both the worktree and the branch go away.
func TestRetentionRemovesMergedWorktree(t *testing.T) {
	repo, _ := gitRepo(t)
	sha := payloadCommit(t, repo, "payload", "deliverable.txt", "merged deliverable\n")
	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "cherry-pick", sha}, Cwd: ".",
		TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone || final.CommitsAhead != 1 {
		t.Fatalf("fixture job = %s with %d commit(s) ahead (err=%s)", final.Status, final.CommitsAhead, final.Error)
	}
	// The reviewer merges the deliverable into the base branch.
	gitOutIn(t, repo, "merge", "--ff-only", final.WorktreeBranch)

	s.config().Storage.Retention = config.RetentionConfig{MaxAgeDays: 1}
	base := time.Now()
	s.nowFn = func() time.Time { return base.Add(48 * time.Hour) }

	deleted, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("pruned %d jobs, want 1", deleted)
	}
	if _, ok, _ := s.meta.GetJob(final.ID); ok {
		t.Fatalf("job %s still in DB after prune", final.ID)
	}
	if isDir(final.WorktreePath) {
		t.Fatalf("merged worktree %s was not removed", final.WorktreePath)
	}
	if out := gitOutIn(t, repo, "branch", "--list", final.WorktreeBranch); out != "" {
		t.Fatalf("merged branch %s was not deleted: %q", final.WorktreeBranch, out)
	}
}

// TestRetentionKeepsUnmergedWorktree proves the sweep never drops a deliverable:
// when the job's branch is not merged into the base branch, pruning the job row
// leaves the worktree and the branch intact.
func TestRetentionKeepsUnmergedWorktree(t *testing.T) {
	repo, _ := gitRepo(t)
	sha := payloadCommit(t, repo, "payload", "deliverable.txt", "unmerged deliverable\n")
	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "cherry-pick", sha}, Cwd: ".",
		TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone || final.CommitsAhead != 1 {
		t.Fatalf("fixture job = %s with %d commit(s) ahead (err=%s)", final.Status, final.CommitsAhead, final.Error)
	}

	s.config().Storage.Retention = config.RetentionConfig{MaxAgeDays: 1}
	base := time.Now()
	s.nowFn = func() time.Time { return base.Add(48 * time.Hour) }

	deleted, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("pruned %d jobs, want 1", deleted)
	}
	if _, ok, _ := s.meta.GetJob(final.ID); ok {
		t.Fatalf("job %s still in DB after prune", final.ID)
	}
	if !isDir(final.WorktreePath) {
		t.Fatalf("unmerged worktree %s must be kept", final.WorktreePath)
	}
	if _, err := os.Stat(filepath.Join(final.WorktreePath, "deliverable.txt")); err != nil {
		t.Fatalf("kept worktree lost the deliverable: %v", err)
	}
	if out := gitOutIn(t, repo, "branch", "--list", final.WorktreeBranch); !strings.Contains(out, final.WorktreeBranch) {
		t.Fatalf("unmerged branch %s must be kept, branch --list = %q", final.WorktreeBranch, out)
	}
}
