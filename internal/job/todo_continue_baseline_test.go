package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// K4b: a todo continuation measures ITS OWN changes from where the previous item
// ended (JobRequest.BaselineSHA); the worktree's branch base (WorktreeBaseSHA) stays.

// enableDiffCapture turns on changes.diff for exec jobs of the "repo" project.
func enableDiffCapture(s *Service) {
	on := true
	proj := s.config().Projects["repo"]
	proj.CaptureDiff = &on
	s.config().Projects["repo"] = proj
}

// readJobChangesDiff returns the job's captured changes.diff.
func readJobChangesDiff(t *testing.T, res JobResult) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(res.ResultDir, "changes.diff"))
	if err != nil {
		t.Fatalf("read changes.diff of %s: %v", res.ID, err)
	}
	return string(b)
}

// TestContinuationBaselineWorktree: the predecessor committed a.txt in a worktree (and
// left README.md modified, uncommitted); the continuation, started with the
// predecessor's end point as its baseline, commits b.txt in the same worktree. Its
// commit list, done line, diff and scope check contain b.txt only; the branch base of
// the worktree is the predecessor's.
func TestContinuationBaselineWorktree(t *testing.T) {
	for _, leftover := range []bool{false, true} {
		name := "predecessor left nothing"
		if leftover {
			name = "predecessor left uncommitted changes"
		}
		t.Run(name, func(t *testing.T) {
			repo, _ := gitRepo(t)
			shaA := payloadCommit(t, repo, "pa", "a.txt", "from the predecessor\n")
			shaB := payloadCommit(t, repo, "pb", "b.txt", "from the continuation\n")
			s := newWorktreeService(t, repo, t.TempDir())
			enableDiffCapture(s)

			pred := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
				Cmd: []string{"git", "cherry-pick", shaA}, Cwd: ".", TimeoutSec: 60, Worktree: true})
			if pred.Status != StatusDone || pred.WorktreePath == "" || pred.WorktreeHeadSHA == "" {
				t.Fatalf("setup: status=%s worktree=%q head=%q err=%s", pred.Status, pred.WorktreePath, pred.WorktreeHeadSHA, pred.Error)
			}
			if leftover {
				writeRepoFile(t, pred.WorktreePath, "README.md", "left uncommitted by the predecessor\n")
			}

			baseline := s.predecessorEndPoint(pred)
			if baseline != pred.WorktreeHeadSHA {
				t.Fatalf("predecessorEndPoint = %q, want the worktree head %q", baseline, pred.WorktreeHeadSHA)
			}
			cont := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
				Cmd: []string{"git", "cherry-pick", shaB}, Cwd: s.resumeCwd(pred), TimeoutSec: 60,
				ResumedFrom: pred.ID, SourceJobID: pred.ID, InheritWorktree: true, BaselineSHA: baseline,
				TodoID: "t-b", Scope: []string{"b.txt"}})
			if cont.Status != StatusDone || cont.WorktreePath != pred.WorktreePath {
				t.Fatalf("continuation: status=%s worktree=%q (want %q) err=%s", cont.Status, cont.WorktreePath, pred.WorktreePath, cont.Error)
			}

			// The baseline is the predecessor's end point; the branch base is untouched.
			if cont.BaseSHA != baseline {
				t.Errorf("BaseSHA = %q, want the baseline %q", cont.BaseSHA, baseline)
			}
			if cont.WorktreeBaseSHA != pred.WorktreeBaseSHA || cont.WorktreeBranch != pred.WorktreeBranch {
				t.Errorf("worktree base/branch = %q/%q, want the predecessor's %q/%q",
					cont.WorktreeBaseSHA, cont.WorktreeBranch, pred.WorktreeBaseSHA, pred.WorktreeBranch)
			}
			if req := requestOfJob(cont); req.BaselineSHA != baseline {
				t.Errorf("request_json baseline_sha = %q, want %q", req.BaselineSHA, baseline)
			}

			// Done line: B only.
			if len(cont.Commits) != 1 || !strings.Contains(cont.Commits[0].Subject, "b.txt") {
				t.Fatalf("commits = %+v, want just the continuation's", cont.Commits)
			}
			line := todoDoneLine(cont)
			if !strings.Contains(line, "1 commits") || !strings.Contains(line, "b.txt") || strings.Contains(line, "a.txt") {
				t.Errorf("done line = %q, want B only", line)
			}

			// Diff and scope: B only, the predecessor's leftover included.
			diff := readJobChangesDiff(t, cont)
			if !strings.Contains(diff, "from the continuation") || strings.Contains(diff, "from the predecessor") {
				t.Errorf("changes.diff is not B only:\n%s", diff)
			}
			if strings.Contains(diff, "README.md") || strings.Contains(cont.DiffSummary, "README.md") {
				t.Errorf("the predecessor's untouched leftover is in the continuation's diff:\n%s\n%s", diff, cont.DiffSummary)
			}
			if strings.Contains(cont.DiffSummary, "a.txt") || !strings.Contains(cont.DiffSummary, "b.txt") {
				t.Errorf("diff summary = %q, want B only", cont.DiffSummary)
			}
			files := DiffFiles(diff)
			if len(files) != 1 || files[0] != "b.txt" {
				t.Errorf("diff files = %v, want [b.txt]", files)
			}
			if out := OutOfScope(files, cont.Scope); len(out) != 0 {
				t.Errorf("out of scope = %v, want none", out)
			}
		})
	}
}

// TestContinuationBaselineCommittedLeftover: a leftover the continuation commits IS its
// work: the commit is on its list and the file in its diff.
func TestContinuationBaselineCommittedLeftover(t *testing.T) {
	repo, _ := gitRepo(t)
	shaA := payloadCommit(t, repo, "pa", "a.txt", "from the predecessor\n")
	s := newWorktreeService(t, repo, t.TempDir())
	enableDiffCapture(s)
	pred := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "cherry-pick", shaA}, Cwd: ".", TimeoutSec: 60, Worktree: true})
	if pred.Status != StatusDone {
		t.Fatalf("setup: %s %s", pred.Status, pred.Error)
	}
	writeRepoFile(t, pred.WorktreePath, "README.md", "left by the predecessor\n")
	cont := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "commit", "-q", "-am", "commit the leftover"}, Cwd: s.resumeCwd(pred), TimeoutSec: 60,
		ResumedFrom: pred.ID, SourceJobID: pred.ID, InheritWorktree: true, BaselineSHA: s.predecessorEndPoint(pred)})
	if cont.Status != StatusDone || len(cont.Commits) != 1 {
		t.Fatalf("continuation: %s commits=%v err=%s", cont.Status, cont.Commits, cont.Error)
	}
	files := DiffFiles(readJobChangesDiff(t, cont))
	if len(files) != 1 || files[0] != "README.md" {
		t.Errorf("diff files = %v, want [README.md] (a.txt belongs to the predecessor)", files)
	}
}

// TestContinuationBaselinePlainCheckout: without a worktree the baseline is a
// predecessor's end commit too, and it wins over the commit captured at start.
func TestContinuationBaselinePlainCheckout(t *testing.T) {
	repo, init := gitRepo(t)
	shaA := payloadCommit(t, repo, "pa", "a.txt", "from the predecessor\n")
	shaB := payloadCommit(t, repo, "pb", "b.txt", "from the continuation\n")
	s := newWorktreeService(t, repo, t.TempDir())
	enableDiffCapture(s)

	pred := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "cherry-pick", shaA}, Cwd: ".", TimeoutSec: 60})
	if pred.Status != StatusDone || len(pred.Commits) != 1 {
		t.Fatalf("setup: %s commits=%v err=%s", pred.Status, pred.Commits, pred.Error)
	}
	head := gitOutIn(t, repo, "rev-parse", "HEAD")
	if got := s.predecessorEndPoint(pred); got != head {
		t.Fatalf("predecessorEndPoint = %q, want HEAD %q (the newest commit it made, expanded)", got, head)
	}
	// A predecessor that committed nothing ended where it started.
	if got := s.predecessorEndPoint(JobResult{Cwd: repo, BaseSHA: init}); got != init {
		t.Fatalf("predecessorEndPoint(no commits) = %q, want its start %q", got, init)
	}
	if got := s.predecessorEndPoint(JobResult{Cwd: repo}); got != "" {
		t.Fatalf("predecessorEndPoint(nothing recorded) = %q, want empty", got)
	}

	writeRepoFile(t, repo, "README.md", "left uncommitted\n")
	cont := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "cherry-pick", shaB}, Cwd: ".", TimeoutSec: 60, BaselineSHA: head})
	if cont.Status != StatusDone || cont.BaseSHA != head || len(cont.Commits) != 1 {
		t.Fatalf("continuation: %s base=%q commits=%v err=%s", cont.Status, cont.BaseSHA, cont.Commits, cont.Error)
	}
	diff := readJobChangesDiff(t, cont)
	files := DiffFiles(diff)
	if len(files) != 1 || files[0] != "b.txt" {
		t.Errorf("diff files = %v, want [b.txt]\n%s", files, diff)
	}

	// An explicit baseline wins over HEAD at start, wherever it points.
	other := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "log", "-1"}, Cwd: ".", TimeoutSec: 60, BaselineSHA: init})
	if other.BaseSHA != init || len(other.Commits) != 2 {
		t.Fatalf("explicit baseline: base=%q commits=%v, want %q and the two commits since", other.BaseSHA, other.Commits, init)
	}
}

// TestBaselineSHAIsValidated: the value ends up in git arguments, so only a commit id passes.
func TestBaselineSHAIsValidated(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	for _, bad := range []string{"--output=x", "HEAD", "main..HEAD", "abc", "zzzzzzzz"} {
		_, err := s.Submit(JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
			Cmd: []string{"git", "log", "-1"}, Cwd: ".", TimeoutSec: 30, BaselineSHA: bad})
		if err == nil || !strings.Contains(err.Error(), "baseline_sha") {
			t.Errorf("BaselineSHA %q: err = %v, want a baseline_sha refusal", bad, err)
		}
	}
}

// TestBaselineSHAHoldRoundTrip: a held request keeps its baseline through the park and
// the approval.
func TestBaselineSHAHoldRoundTrip(t *testing.T) {
	root := t.TempDir()
	s := newHoldService(t, root, nil)
	sha := strings.Repeat("ab", 20)
	req := probeRequest("echo", filepath.Join(root, "probe"))
	req.BaselineSHA = sha
	res := mustHold(t, s, req, "")
	if got := requestOfJob(res).BaselineSHA; got != sha {
		t.Fatalf("parked request_json baseline_sha = %q, want %q", got, sha)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(res.RequestJSON), &raw); err != nil || raw["baseline_sha"] != sha {
		t.Fatalf("request_json = %s (err=%v)", res.RequestJSON, err)
	}
	rec, _, err := s.heldRecord(res.ID)
	if err != nil {
		t.Fatalf("heldRecord: %v", err)
	}
	var back JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &back); err != nil || back.BaselineSHA != sha {
		t.Fatalf("stored request baseline = %q (err=%v), want %q", back.BaselineSHA, err, sha)
	}
}

// TestNormalResumeKeepsWorktreeBase: a normal resume of a worktree job (and the
// continuation reject --resume makes) measures from the worktree base as before: no
// baseline, WorktreeBaseSHA carried over.
func TestNormalResumeKeepsWorktreeBase(t *testing.T) {
	repo, _ := gitRepo(t)
	first := payloadCommit(t, repo, "p1", "one.txt", "one\n")
	second := payloadCommit(t, repo, "p2", "two.txt", "two\n")
	s := newPickService(t, repo, t.TempDir())
	src := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "picker", Runner: "local",
		Prompt: first, Cwd: ".", TimeoutSec: 60, Worktree: true, SessionID: "sess-wt", RulesResolved: true})
	if src.Status != StatusDone || src.WorktreePath == "" {
		t.Fatalf("setup: %s %q %s", src.Status, src.WorktreePath, src.Error)
	}
	res, err := s.ResumeJob(src.ID, second, "", "")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	cont, _ := s.Wait(res.ID)
	if cont.Status != StatusDone {
		t.Fatalf("resume: %s %s", cont.Status, cont.Error)
	}
	if cont.WorktreeBaseSHA != src.WorktreeBaseSHA || cont.BaseSHA != src.WorktreeBaseSHA {
		t.Errorf("worktree base/base = %q/%q, want the source's %q for both", cont.WorktreeBaseSHA, cont.BaseSHA, src.WorktreeBaseSHA)
	}
	if req := requestOfJob(cont); req.BaselineSHA != "" {
		t.Errorf("a normal resume must not set a baseline, got %q", req.BaselineSHA)
	}
	if len(cont.Commits) != 2 {
		t.Errorf("commits = %v, want both (the branch's since its base)", cont.Commits)
	}
}

// TestTodoContinuationRequestSetsBaseline: the request the decision builds carries the
// predecessor's end point — its worktree head, else its newest commit.
func TestTodoContinuationRequestSetsBaseline(t *testing.T) {
	s, root := newContinueService(t)
	initGitRepo(t, root)
	head := gitOutIn(t, root, "rev-parse", "HEAD")
	seedContinuePlan(t, s, nil)

	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
		j.Cwd = root
		j.BaseSHA = "0000000000000000000000000000000000000000"
		j.Commits = []Commit{{SHA: head[:9], Subject: "pred work"}}
	})
	todo, _, _ := s.meta.GetTodo(contSucc)
	plan, _, _ := s.meta.GetPlan(contPlan)
	normal, why := s.normalTodoRequest(plan, todo, "")
	if why != "" {
		t.Fatal(why)
	}
	req, reason := s.todoContinuationRequest(plan, todo, normal)
	if reason != "" {
		t.Fatalf("decision: %s", reason)
	}
	if req.BaselineSHA != head {
		t.Fatalf("BaselineSHA = %q, want the predecessor's newest commit %q (expanded)", req.BaselineSHA, head)
	}
}

// TestFallbackWorktreeBasedOnPredecessorBranch: the chain breaks (another session
// family) and the predecessor left an unmerged worktree branch: the item's new worktree
// starts at that branch, and continue_note says so.
func TestFallbackWorktreeBasedOnPredecessorBranch(t *testing.T) {
	s, root := newContinueService(t)
	initGitRepo(t, root)
	gitOutIn(t, root, "config", "user.email", "t@t")
	gitOutIn(t, root, "config", "user.name", "t")
	gitOutIn(t, root, "add", "-A")
	gitOutIn(t, root, "commit", "-q", "-m", "tidy")
	gitOutIn(t, root, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("tmp/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutIn(t, root, "add", ".gitignore")
	gitOutIn(t, root, "commit", "-q", "-m", "ignore tmp")

	ctx := context.Background()
	wt, err := prepareJobWorktree(ctx, root, "j-pred", "")
	if err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	writeRepoFile(t, wt.Path, "pred.txt", "predecessor's commit\n")
	gitOutIn(t, wt.Path, "add", "pred.txt")
	gitOutIn(t, wt.Path, "commit", "-q", "-m", "pred work")
	predHead := gitOutIn(t, wt.Path, "rev-parse", "HEAD")

	proj := s.config().Projects["self"]
	proj.WorktreeDefault = true
	s.config().Projects["self"] = proj

	seedContinuePlan(t, s, func(td *jobstore.PlanTodo) { td.Assignee = "cx" }) // another family: no continuation
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
		j.Cwd, j.WorktreePath, j.WorktreeBranch = wt.Path, wt.Path, wt.Branch
		j.WorktreeBaseSHA, j.WorktreeHeadSHA, j.CommitsAhead = wt.BaseSHA, predHead, 1
	})

	d, derr := s.DispatchTodo(contSucc, "")
	final := dispatchedJob(t, s, d, derr)
	if final.ResumedFrom != "" || final.WorktreeBranch == "" || final.WorktreeBranch == wt.Branch {
		t.Fatalf("job = resumed_from %q worktree branch %q, want a fresh session in a NEW worktree", final.ResumedFrom, final.WorktreeBranch)
	}
	if final.WorktreeBaseSHA != predHead {
		t.Fatalf("new worktree base = %q, want the predecessor's branch head %q", final.WorktreeBaseSHA, predHead)
	}
	if _, err := os.Stat(filepath.Join(final.WorktreePath, "pred.txt")); err != nil {
		t.Errorf("the predecessor's commit is not visible in the new worktree: %v", err)
	}
	got, _, _ := s.meta.GetTodo(contSucc)
	if !strings.Contains(got.ContinueNote, "未接续：") || !strings.Contains(got.ContinueNote, "以前序分支 "+wt.Branch+" 为基") {
		t.Fatalf("continue_note = %q, want the fallback reason and the branch it is based on", got.ContinueNote)
	}
	evs := continueFallbackEvents(t, s)
	if len(evs) != 1 || !strings.Contains(evs[0].Detail, wt.Branch) {
		t.Fatalf("job.continue_fallback events = %+v", evs)
	}
}

// TestFallbackWorktreeBaseOnlyWhenUnmerged: nothing is based on a branch that is merged
// already, or when the item does not run in a worktree.
func TestFallbackWorktreeBaseOnlyWhenUnmerged(t *testing.T) {
	s, root := newContinueService(t)
	initGitRepo(t, root)
	seedContinuePlan(t, s, func(td *jobstore.PlanTodo) { td.Assignee = "cx" })
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
		j.Cwd, j.WorktreePath, j.WorktreeBranch = root, filepath.Join(root, "gone"), "gofer/j-pred"
	})
	todo, _, _ := s.meta.GetTodo(contSucc)
	req := JobRequest{ProjectKey: "self", Runner: "local"}
	if note, branch := s.basePredecessorBranch(todo, &req); note != "" || branch != "" || req.WorktreeBase != "" {
		t.Fatalf("no worktree wanted / worktree gone: note=%q branch=%q base=%q", note, branch, req.WorktreeBase)
	}
	proj := s.config().Projects["self"]
	proj.WorktreeDefault = true
	s.config().Projects["self"] = proj
	if note, _ := s.basePredecessorBranch(todo, &req); note != "" || req.WorktreeBase != "" {
		t.Fatalf("worktree directory is gone: note=%q base=%q", note, req.WorktreeBase)
	}
}
