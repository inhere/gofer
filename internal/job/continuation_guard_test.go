package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// Continuation worktree guard (gofer-fi7f). The fixtures use a real git repository and
// real managed worktrees; "ra" is a resumable agent that finishes at once (testcmd argv).

type guardEnv struct {
	s    *Service
	repo string
	bin  string
}

func newGuardEnv(t *testing.T, mutate func(*config.Config)) *guardEnv {
	t.Helper()
	repo, _ := gitRepo(t)
	state := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: state},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath: repo, AllowedAgents: []string{"ra", "rb", "exec"},
				AllowedRunners: []string{"local"}, AllowExec: true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"ra": {
				Type: agent.TypeCLIAgent, Command: bin, Args: []string{"argv", "{{prompt}}"},
				SessionFamily: "fam", SessionResume: []string{"argv", "--resume", "{{session_id}}", "{{prompt}}"},
			},
			"rb": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"argv", "{{prompt}}"}},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return &guardEnv{s: newServiceFromCfg(t, state, cfg), repo: repo, bin: bin}
}

// source runs a --worktree job to completion and turns its row into a resumable "ra"
// session, the way a worktree agent job looks after it ended.
func (e *guardEnv) source(t *testing.T) JobResult {
	t.Helper()
	src := submitAndWait(t, e.s, JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{e.bin, "printf", "ok"}, Cwd: ".", TimeoutSec: 60, Worktree: true})
	if src.Status != StatusDone || src.WorktreePath == "" || src.WorktreeBranch == "" {
		t.Fatalf("setup: status=%s worktree=%q branch=%q err=%s", src.Status, src.WorktreePath, src.WorktreeBranch, src.Error)
	}
	src.Agent, src.SessionID = "ra", "guard-session"
	src.RequestJSON = `{"project_key":"self","agent":"ra","runner":"local","prompt":"work","cwd":".","timeout_sec":30}`
	if err := e.s.persist(src); err != nil {
		t.Fatalf("persist source: %v", err)
	}
	return src
}

// cleanup removes the source's worktree and branch the way a merge cleanup does (the
// row keeps WorktreeBranch, WorktreePath is cleared).
func (e *guardEnv) cleanup(t *testing.T, src JobResult) JobResult {
	t.Helper()
	if _, err := e.s.RemoveWorktree(src.ID, true, true); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	got, _ := e.s.Get(src.ID)
	if got.WorktreePath != "" || got.WorktreeBranch == "" {
		t.Fatalf("after cleanup path=%q branch=%q, want the path cleared and the branch kept", got.WorktreePath, got.WorktreeBranch)
	}
	return got
}

func (e *guardEnv) jobCount(t *testing.T) int {
	t.Helper()
	jobs, err := e.s.ListJobs(ListOpts{Project: "self"})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	return len(jobs)
}

func (e *guardEnv) mainClean(t *testing.T) {
	t.Helper()
	if out := gitOutIn(t, e.repo, "status", "--porcelain"); out != "" {
		t.Fatalf("main checkout is not clean:\n%s", out)
	}
}

func wantGone(t *testing.T, err error, reason string) {
	t.Helper()
	if !errors.Is(err, ErrContinuationWorktreeGone) {
		t.Fatalf("error = %v, want ErrContinuationWorktreeGone", err)
	}
	var cwe *ContinuationWorktreeError
	if !errors.As(err, &cwe) || cwe.Reason != reason {
		t.Fatalf("error = %v, want reason %q", err, reason)
	}
}

func wantSkipped(t *testing.T, s *Service, srcID, entry string) {
	t.Helper()
	detail := waitEvent(t, s, srcID, EventJobContinuationSkipped)
	if !strings.Contains(detail, `"entry":"`+entry+`"`) || !strings.Contains(detail, `"reason":"worktree_gone"`) {
		t.Fatalf("job.continuation_skipped detail = %q, want entry %s / reason worktree_gone", detail, entry)
	}
}

func TestContinuationAfterCleanupRefused(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.cleanup(t, e.source(t))
	before := e.jobCount(t)
	_, err := e.s.ResumeJob(src.ID, "continue", "", "caller")
	wantGone(t, err, reasonWorktreeGone)
	if after := e.jobCount(t); after != before {
		t.Fatalf("jobs %d -> %d, a refused resume must not leave a job", before, after)
	}
	e.mainClean(t)
}

func TestContinuationLeftoverDirWithoutGitRefused(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.source(t)
	// The registration is gone and the directory could not be deleted: no .git inside,
	// so git run there would climb up to the main repository.
	if err := os.Remove(filepath.Join(src.WorktreePath, ".git")); err != nil {
		t.Fatalf("remove .git: %v", err)
	}
	before := e.jobCount(t)
	_, err := e.s.ResumeJob(src.ID, "continue", "", "caller")
	wantGone(t, err, reasonWorktreeGone)
	if after := e.jobCount(t); after != before {
		t.Fatalf("jobs %d -> %d", before, after)
	}
	e.mainClean(t)
}

func TestContinuationBranchRefDeletedRefused(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.source(t)
	gitOutIn(t, e.repo, "update-ref", "-d", "refs/heads/"+src.WorktreeBranch)
	_, err := e.s.ResumeJob(src.ID, "continue", "", "caller")
	wantGone(t, err, reasonWorktreeGone)
	e.mainClean(t)
}

func TestContinuationDetachedHeadStillInherits(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.source(t)
	gitOutIn(t, src.WorktreePath, "checkout", "--detach")
	res, err := e.s.ResumeJob(src.ID, "continue", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob on a detached HEAD: %v", err)
	}
	if res.WorktreePath != src.WorktreePath {
		t.Fatalf("continuation worktree = %q, want the source's %q", res.WorktreePath, src.WorktreePath)
	}
	if final, _ := e.s.Wait(res.ID); final.Status != StatusDone {
		t.Fatalf("continuation = %s (%s), want done", final.Status, final.Error)
	}
}

func TestContinuationWorktreeOutsideProject(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.source(t)
	// A cwd outside the worktree (the project root fell back from a worktree that is
	// not under it) must not run as if it were inside.
	_, err := e.s.continuationWorktree(src.ID, e.repo)
	wantGone(t, err, reasonWorktreeOutsideProject)
	wt, err := e.s.continuationWorktree(src.ID, src.WorktreePath)
	if err != nil || wt == nil || wt.Path != src.WorktreePath || wt.Branch != src.WorktreeBranch {
		t.Fatalf("inside the worktree: wt=%+v err=%v, want the source's worktree", wt, err)
	}
	plain := submitAndWait(t, e.s, JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{e.bin, "printf", "ok"}, Cwd: ".", TimeoutSec: 60})
	if wt, err := e.s.continuationWorktree(plain.ID, e.repo); wt != nil || err != nil {
		t.Fatalf("a plain source = (%v, %v), want (nil, nil)", wt, err)
	}
}

func TestContinuationAutoResumeSkipped(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.cleanup(t, e.source(t))
	if e.s.autoResume(src, "rate limit") {
		t.Fatal("autoResume submitted a continuation for a worktree that is gone")
	}
	wantSkipped(t, e.s, src.ID, "auto")
	if got, _ := e.s.Get(src.ID); got.AutoResumedBy != "" {
		t.Fatalf("AutoResumedBy = %q, want none", got.AutoResumedBy)
	}
	e.mainClean(t)
}

func TestContinuationUncommittedResumeSkipped(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.cleanup(t, e.source(t))
	src.UncommittedCount, src.UncommittedFiles = 1, []string{"a.txt"}
	if e.s.resumeUncommitted(src) {
		t.Fatal("resumeUncommitted submitted a continuation for a worktree that is gone")
	}
	wantSkipped(t, e.s, src.ID, "uncommitted")
	e.mainClean(t)
}

func TestContinuationFollowUpSkipped(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.cleanup(t, e.source(t))
	if _, err := e.s.Meta().AddFollowUp(src.ID, "one more thing", "caller", false); err != nil {
		t.Fatal(err)
	}
	e.s.processFollowUps(src)
	rows, err := e.s.Meta().ListFollowUps(src.ID)
	if err != nil || len(rows) != 1 || rows[0].Status != jobstore.FollowUpPending {
		t.Fatalf("follow-ups = %+v (err=%v), want the one released back to pending", rows, err)
	}
	wantSkipped(t, e.s, src.ID, "followup")
	e.mainClean(t)
}

func TestContinuationFallbackSkippedAndInherits(t *testing.T) {
	e := newGuardEnv(t, nil)
	// Usable worktree: the takeover runs in it and its row records the path, so the
	// merge / remove checks see it.
	src := e.source(t)
	if !e.s.fallBack(src, "rate limit", "rb") {
		t.Fatal("fallBack refused a takeover although the worktree is usable")
	}
	got, _ := e.s.Get(src.ID)
	next, _ := e.s.Wait(got.FellBackTo)
	if next.WorktreePath != src.WorktreePath || next.Status != StatusDone {
		t.Fatalf("takeover worktree=%q status=%s (%s), want the source's %q and done", next.WorktreePath, next.Status, next.Error, src.WorktreePath)
	}
	// Gone worktree: no takeover, the source records why.
	gone := e.cleanup(t, e.source(t))
	before := e.jobCount(t)
	if e.s.fallBack(gone, "rate limit", "rb") {
		t.Fatal("fallBack submitted a takeover for a worktree that is gone")
	}
	wantSkipped(t, e.s, gone.ID, "fallback")
	if after := e.jobCount(t); after != before {
		t.Fatalf("jobs %d -> %d", before, after)
	}
	e.mainClean(t)
}

// A delivery-check continuation that the guard refuses comes back as an error carrying
// the reason; execute parks the job with it (parkAfterFailedCheckResume, unchanged).
func TestContinuationDeliveryCheckResumeRefused(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.cleanup(t, e.source(t))
	err := e.s.resumeAfterCheck(src, checkDecision{})
	wantGone(t, err, reasonWorktreeGone)
	if !strings.Contains(err.Error(), reasonWorktreeGone) {
		t.Fatalf("error %q does not carry the reason", err)
	}
}

func TestContinuationHoldUpgradeKeepsWorktree(t *testing.T) {
	// A continuation held before the upgrade stored only inherit_worktree.
	var h heldInternals
	if err := json.Unmarshal([]byte(`{"inherit_worktree":true}`), &h); err != nil {
		t.Fatal(err)
	}
	req := JobRequest{ResumedFrom: "job-src"}
	h.restore(&req)
	if req.WorktreeFrom != "job-src" {
		t.Fatalf("WorktreeFrom = %q, want the legacy flag to resolve to ResumedFrom", req.WorktreeFrom)
	}
	// The new shape round-trips by itself.
	cap := captureHeldInternals(&JobRequest{WorktreeFrom: "job-x", ResumedFrom: "job-y"})
	back := JobRequest{}
	cap.restore(&back)
	if back.WorktreeFrom != "job-x" {
		t.Fatalf("round trip WorktreeFrom = %q, want job-x", back.WorktreeFrom)
	}
}

func TestContinuationFailureDecisionClass(t *testing.T) {
	e := newGuardEnv(t, nil)
	err := &ContinuationWorktreeError{Reason: reasonWorktreeGone, Path: "/x"}
	dec := e.s.failureDecision(JobResult{Error: err.Error(), SessionID: "s", Agent: "ra"})
	if dec.Class != FailureClassWorktreeGone || dec.AutoResume || dec.Fallback != "" {
		t.Fatalf("decision = %+v, want class worktree-gone with no takeover", dec)
	}
}

// A worktree-gone failure is never scheduled for retry; the same failure without that
// class is (so the assertion discriminates).
func TestContinuationRetrySkippedForWorktreeGone(t *testing.T) {
	e := newGuardEnv(t, nil)
	reqJSON, _ := json.Marshal(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{e.bin, "exit", "7"}, Cwd: ".", TimeoutSec: 30,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}}})
	snap := JobResult{ID: "job-retry-ctl", ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: StatusFailed, ExitCode: 7, RequestJSON: string(reqJSON), Attempt: 1}
	if err := e.s.persist(snap); err != nil {
		t.Fatal(err)
	}
	e.s.maybeRetryJob(snap)
	if rows := retryRows(t, e.s, snap.ID); len(rows) != 1 {
		t.Fatalf("control: retry rows = %+v, want one", rows)
	}
	gone := snap
	gone.ID, gone.FailureClass = "job-retry-gone", FailureClassWorktreeGone
	if err := e.s.persist(gone); err != nil {
		t.Fatal(err)
	}
	e.s.maybeRetryJob(gone)
	if rows := retryRows(t, e.s, gone.ID); len(rows) != 0 {
		t.Fatalf("retry rows = %+v, want none for a worktree-gone failure", rows)
	}
}

func (e *guardEnv) seedRetry(t *testing.T, src JobResult, req JobRequest) jobstore.RetryRecord {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	rec := jobstore.RetryRecord{ID: jobstore.NewRetryID(), SourceJobID: src.ID, Attempt: 2,
		RequestJSON: string(b), Reason: "exit_code=7", NextRunAt: now - 1, CreatedAt: now}
	if err := e.s.Meta().InsertRetry(rec); err != nil {
		t.Fatalf("InsertRetry: %v", err)
	}
	return rec
}

// A retry of a continuation / takeover job (its request has worktree=false but the job
// ran in a worktree) is refused once that worktree is gone; a retry of a first
// --worktree job still creates a new worktree (unchanged).
func TestContinuationRetryRefusedAfterCleanup(t *testing.T) {
	e := newGuardEnv(t, nil)
	src := e.cleanup(t, e.source(t))
	rec := e.seedRetry(t, src, JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{e.bin, "printf", "ok"}, Cwd: ".", TimeoutSec: 30})
	before := e.jobCount(t)
	started, failed, err := e.s.SweepDueRetries(time.Now().Unix(), 10, 60)
	if err != nil || started != 0 || failed != 1 {
		t.Fatalf("sweep started/failed = %d/%d (err=%v), want 0/1", started, failed, err)
	}
	rows := retryRows(t, e.s, src.ID)
	if len(rows) != 1 || rows[0].ID != rec.ID || rows[0].State != jobstore.RetryCancelled {
		t.Fatalf("retry rows = %+v, want the row cancelled", rows)
	}
	wantSkipped(t, e.s, src.ID, "retry")
	if d := waitEvent(t, e.s, src.ID, EventJobRetryCancelled); !strings.Contains(d, rec.ID) {
		t.Fatalf("job.retry_cancelled detail = %q", d)
	}
	if after := e.jobCount(t); after != before {
		t.Fatalf("jobs %d -> %d", before, after)
	}

	// First --worktree job: the request says worktree=true, the retry makes its own.
	src2 := e.cleanup(t, e.source(t))
	e.seedRetry(t, src2, JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{e.bin, "printf", "ok"}, Cwd: ".", TimeoutSec: 30, Worktree: true})
	started, failed, err = e.s.SweepDueRetries(time.Now().Unix(), 10, 60)
	if err != nil || started != 1 || failed != 0 {
		t.Fatalf("sweep 2 started/failed = %d/%d (err=%v), want 1/0", started, failed, err)
	}
	rows = retryRows(t, e.s, src2.ID)
	if len(rows) != 1 || rows[0].NewJobID == "" {
		t.Fatalf("retry rows = %+v", rows)
	}
	final, _ := e.s.Wait(rows[0].NewJobID)
	if final.Status != StatusDone || final.WorktreePath == "" {
		t.Fatalf("retried first-worktree job = %s worktree=%q (%s), want done in a new worktree", final.Status, final.WorktreePath, final.Error)
	}
}

// The worktree is deleted after the continuation was submitted and before it ran: the
// pre-run recheck fails it as worktree-gone, no agent starts and nothing takes it over.
func TestContinuationWorktreeDeletedWhileQueued(t *testing.T) {
	e := newGuardEnv(t, func(cfg *config.Config) {
		p := cfg.Projects["self"]
		p.MaxConcurrentJobs = 1
		cfg.Projects["self"] = p
	})
	src := e.source(t)
	release := filepath.Join(t.TempDir(), "release")
	blocker, err := e.s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{e.bin, "wait-file", release, "60s"}, Cwd: ".", TimeoutSec: 90, ExclusiveDir: boolPtr(false)})
	if err != nil {
		t.Fatalf("submit blocker: %v", err)
	}
	waitForStatus(t, e.s, blocker.ID, StatusRunning, 10*time.Second)
	cont, err := e.s.ResumeJob(src.ID, "continue", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if got, _ := e.s.Get(cont.ID); got.Status != StatusQueued {
		t.Fatalf("continuation status = %s, want queued behind the blocker", got.Status)
	}
	// Delete the checkout behind the queued continuation's back (git directly: the
	// service's own remove refuses a worktree a live job uses).
	gitOutIn(t, e.repo, "worktree", "remove", "--force", src.WorktreePath)
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	final, _ := e.s.Wait(cont.ID)
	if final.Status != StatusFailed || final.FailureClass != FailureClassWorktreeGone {
		t.Fatalf("continuation = %s class=%q (%s), want failed / worktree-gone", final.Status, final.FailureClass, final.Error)
	}
	if out, _ := e.s.TailLog(cont.ID, store.StreamStdout, 4096); len(out) != 0 {
		t.Fatalf("the agent started: stdout = %q", out)
	}
	if bf, _ := e.s.Wait(blocker.ID); bf.Status != StatusDone {
		t.Fatalf("blocker = %s", bf.Status)
	}
	wait.For(t, 10*time.Second, "no takeover of the refused continuation", func() (bool, any) {
		got, _ := e.s.Get(cont.ID)
		return got.AutoResumedBy == "" && got.FellBackTo == "", got
	})
	if rows := retryRows(t, e.s, cont.ID); len(rows) != 0 {
		t.Fatalf("retry rows = %+v, want none", rows)
	}
}

// todoContinueGone seeds a predecessor that was a worktree job whose worktree is no
// longer usable (a plain directory that is not a checkout of it).
func todoContinueGone(t *testing.T) (*Service, string) {
	t.Helper()
	s, root := newContinueService(t)
	seedContinuePlan(t, s, nil)
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
		j.WorktreePath = root
		j.WorktreeBranch = "gofer/j-pred"
	})
	return s, root
}

// Unbound plan: the refusal is a dispatch failure recorded on the todo.
func TestContinuationTodoDispatchRefusedUnbound(t *testing.T) {
	s, _ := todoContinueGone(t)
	d, err := s.DispatchTodo(contSucc, "")
	if err != nil {
		t.Fatalf("DispatchTodo: %v", err)
	}
	if d.Job != nil || !strings.Contains(d.Reason, reasonWorktreeGone) {
		t.Fatalf("dispatch = job %v reason %q, want no job and a worktree_gone reason", d.Job, d.Reason)
	}
	if td := getTodo(t, s, contSucc); !strings.Contains(td.DispatchError, reasonWorktreeGone) {
		t.Fatalf("todo dispatch_error = %q", td.DispatchError)
	}
}

// Plan bound to a supervising session: an explicit dispatch returns the error (the
// HTTP layer maps it to 409) and starts no job.
func TestContinuationTodoDispatchRefusedBound(t *testing.T) {
	s, root := newContinueService(t)
	if _, err := s.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "sup-1", Agent: "suag", ProjectKey: "self", Runner: "local", Cwd: root, CallerID: "caller-a",
	}); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		s.Meta().InsertPlan(jobstore.Plan{PlanID: contPlan, Title: "Continue plan", ProjectKey: "self",
			Status: jobstore.PlanOpen, Owner: "caller-a", SupervisorSessionID: "sup-1", CreatedAt: 1, UpdatedAt: 1}),
		s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: contPred, PlanID: contPlan, Title: "pred item",
			Status: jobstore.TodoDone, Assignee: "ca", CreatedAt: 1, UpdatedAt: 1}),
		s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: contSucc, PlanID: contPlan, Title: "succ item",
			Assignee: "ca", ContinueFrom: contPred, CreatedAt: 2, UpdatedAt: 2}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
		j.WorktreePath = root
		j.WorktreeBranch = "gofer/j-pred"
	})
	_, err := s.DispatchTodo(contSucc, "caller-a")
	if !errors.Is(err, ErrContinuationWorktreeGone) {
		t.Fatalf("DispatchTodo error = %v, want ErrContinuationWorktreeGone", err)
	}
	jobs, _ := s.ListJobs(ListOpts{Project: "self"})
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want only the predecessor", len(jobs))
	}
}
