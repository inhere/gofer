package job

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/jobstore"
)

// mergePlanWorktreeJob runs one worktree job for todo-m of a complete_on=merge plan
// (paused, so every chain advance shows up as a plan.advance_paused event). The job
// commits one file when commit is true. deliverMode is the project's delivery mode.
func mergePlanWorktreeJob(t *testing.T, deliverMode string, commit bool) (*Service, JobResult, string) {
	t.Helper()
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	if deliverMode != "" {
		s.cfg.Load().Projects["repo"] = func(p config.ProjectConfig) config.ProjectConfig {
			p.Delivery = &config.DeliveryConfig{Mode: deliverMode}
			return p
		}(s.cfg.Load().Projects["repo"])
	}
	seedMergePlanTodo(t, s, "plan-m", "todo-m")
	cmd := []string{"git", "status"}
	if commit {
		sha := payloadCommit(t, repo, "payload", "m.txt", "m\n")
		cmd = []string{"git", "-c", "user.email=t@e.x", "-c", "user.name=t", "cherry-pick", sha}
	}
	j := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local", TodoID: "todo-m",
		Cmd: cmd, Cwd: ".", TimeoutSec: 60, Worktree: true,
	})
	s.WaitTerminalHooks(10 * time.Second)
	return s, j, repo
}

// seedMergePlanTodo inserts a paused complete_on=merge plan with one todo.
func seedMergePlanTodo(t *testing.T, s *Service, planID, todoID string) {
	t.Helper()
	if err := s.Meta().InsertPlan(jobstore.Plan{PlanID: planID, Title: planID, CompleteOn: jobstore.PlanCompleteOnMerge}); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	if err := s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: todoID, PlanID: planID, Title: todoID}); err != nil {
		t.Fatalf("insert todo: %v", err)
	}
	if _, err := s.PausePlan(planID); err != nil {
		t.Fatalf("PausePlan: %v", err)
	}
}

func advanceEvents(t *testing.T, s *Service) int {
	t.Helper()
	return countPlanEvent(t, s, "plan-m", EventPlanAdvancePaused)
}

// TestMergeModeDeliveryNoneWaitsForWorktreeMerge: complete_on=merge, delivery none: the
// finished worktree job leaves the todo doing with a "awaiting merge" note and does not
// advance the plan; `worktree merge` (no --todo-done) then completes it and advances.
func TestMergeModeDeliveryNoneWaitsForWorktreeMerge(t *testing.T) {
	s, j, _ := mergePlanWorktreeJob(t, "", true)
	if j.Status != StatusDone || j.CommitsAhead != 1 {
		t.Fatalf("job = %s ahead=%d", j.Status, j.CommitsAhead)
	}
	td := getTodo(t, s, "todo-m")
	if td.Status != jobstore.TodoDoing || !strings.Contains(td.Note, "待合并") || !strings.Contains(td.Note, j.WorktreeBranch) {
		t.Fatalf("todo = %q %q, want doing + awaiting-merge note naming the branch", td.Status, td.Note)
	}
	if n := advanceEvents(t, s); n != 0 {
		t.Fatalf("plan advanced %d times before the merge", n)
	}
	res, err := s.MergeWorktree(j.ID, MergeOptions{})
	if err != nil || !res.Merged || !res.TodoDone || res.TodoID != "todo-m" {
		t.Fatalf("merge = %+v err=%v, want merged and todo done without --todo-done", res, err)
	}
	if td := getTodo(t, s, "todo-m"); td.Status != jobstore.TodoDone {
		t.Fatalf("todo after merge = %q, want done", td.Status)
	}
	if n := advanceEvents(t, s); n != 1 {
		t.Fatalf("plan advanced %d times after the merge, want 1", n)
	}
}

// TestJobModeWorktreeMergeNeedsTodoDoneFlag: the default `job` mode is unchanged: the
// todo is done when the job is, and `worktree merge` without --todo-done touches nothing.
func TestJobModeWorktreeMergeNeedsTodoDoneFlag(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	j := todoWorktreeJob(t, s, "jm.txt") // todo-w of a default-mode plan, reset to doing
	if _, err := s.MergeWorktree(j.ID, MergeOptions{}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if td := getTodo(t, s, "todo-w"); td.Status != jobstore.TodoDoing {
		t.Fatalf("todo = %q, want doing (job mode needs --todo-done)", td.Status)
	}
}

// TestMergeModeDeliveryMergeCompletesOnMerged: delivery merge — the todo is doing until
// the delivery lands as merged, then done and the plan advances once.
func TestMergeModeDeliveryMergeCompletesOnMerged(t *testing.T) {
	s, j, _ := mergePlanWorktreeJob(t, "merge", true)
	got, _ := s.Get(j.ID)
	if got.Delivery == nil || got.Delivery.State != delivery.StateMerged {
		t.Fatalf("delivery = %+v, want merged", got.Delivery)
	}
	if td := getTodo(t, s, "todo-m"); td.Status != jobstore.TodoDone {
		t.Fatalf("todo = %q %q, want done after the merged delivery", td.Status, td.Note)
	}
	if n := advanceEvents(t, s); n != 1 {
		t.Fatalf("plan advanced %d times, want exactly 1", n)
	}
	// Idempotent: recording the same final delivery again changes nothing.
	if err := s.recordDelivery(j.ID, *got.Delivery); err != nil {
		t.Fatal(err)
	}
	if n := advanceEvents(t, s); n != 1 {
		t.Fatalf("a repeated delivery record advanced the plan again (%d)", n)
	}
}

// TestMergeModeDeliveryNotMergedKeepsDoing: failed / pending / pushed / PR deliveries do
// not complete the todo; failed and pushed / PR say why, pending says nothing.
func TestMergeModeDeliveryNotMergedKeepsDoing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		res      DeliveryResult
		wantNote string
	}{
		{"failed", DeliveryResult{Mode: "merge", State: delivery.StateFailed, Error: "conflict in README"}, "conflict in README"},
		{"pending", DeliveryResult{Mode: "merge", State: delivery.StatePending}, ""},
		{"pushed", DeliveryResult{Mode: "push", State: delivery.StatePushed, Branch: "feat/x"}, "已推送"},
		{"pr", DeliveryResult{Mode: "pr", State: delivery.StatePROpened, PRURL: "https://example.test/pr/1"}, "https://example.test/pr/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, j, _ := mergePlanWorktreeJob(t, "", true)
			before := getTodo(t, s, "todo-m").Note
			if err := s.recordDelivery(j.ID, tc.res); err != nil {
				t.Fatal(err)
			}
			td := getTodo(t, s, "todo-m")
			if td.Status != jobstore.TodoDoing {
				t.Fatalf("todo = %q, want doing", td.Status)
			}
			if tc.wantNote == "" && td.Note != before {
				t.Fatalf("pending changed the note: %q -> %q", before, td.Note)
			}
			if !strings.Contains(td.Note, tc.wantNote) {
				t.Fatalf("note %q lacks %q", td.Note, tc.wantNote)
			}
			if n := advanceEvents(t, s); n != 0 {
				t.Fatalf("plan advanced %d times", n)
			}
		})
	}
}

// TestMergeModeNoCommitsAndNonWorktreeComplete: nothing to deliver (CommitsAhead 0) and a
// job that is not a worktree job complete at job end, like the default mode.
func TestMergeModeNoCommitsAndNonWorktreeComplete(t *testing.T) {
	s, j, _ := mergePlanWorktreeJob(t, "", false)
	if j.CommitsAhead != 0 {
		t.Fatalf("ahead = %d", j.CommitsAhead)
	}
	td := getTodo(t, s, "todo-m")
	if td.Status != jobstore.TodoDone || !strings.Contains(td.Note, "无改动") {
		t.Fatalf("no-commit todo = %q %q, want done + 无改动", td.Status, td.Note)
	}
	if n := advanceEvents(t, s); n != 1 {
		t.Fatalf("plan advanced %d times, want 1", n)
	}

	s2 := newTestService(t, t.TempDir())
	seedMergePlanTodo(t, s2, "plan-m", "todo-n")
	submitAndWait(t, s2, JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", TodoID: "todo-n",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30})
	s2.WaitTerminalHooks(10 * time.Second)
	if td := getTodo(t, s2, "todo-n"); td.Status != jobstore.TodoDone {
		t.Fatalf("non-worktree todo = %q, want done", td.Status)
	}
}

// TestTodoCompletionDecisions pins the decision table for the cases a unit can build
// without running a job: remote worker worktrees, a continuation in flight, job mode.
func TestTodoCompletionDecisions(t *testing.T) {
	s := newTestService(t, t.TempDir())
	s.cfg.Load().Runners = map[string]config.RunnerConfig{"w1": {Type: "worker"}}
	merge := jobstore.Plan{PlanID: "p", CompleteOn: jobstore.PlanCompleteOnMerge}
	jobMode := jobstore.Plan{PlanID: "p"}
	wt := JobResult{ID: "j1", Status: StatusDone, WorktreeBranch: "gofer/j1", CommitsAhead: 2}

	remote := wt
	remote.Runner = "w1"
	if d := s.todoCompletion(remote, merge, false); !d.complete || !strings.Contains(d.note, "远程") {
		t.Fatalf("remote worktree job = %+v, want complete with the remote note", d)
	}
	if d := s.todoCompletion(wt, merge, true); d.complete || d.note != "" {
		t.Fatalf("resume pending (merge) = %+v, want nothing", d)
	}
	if d := s.todoCompletion(wt, jobMode, true); d.complete {
		t.Fatalf("resume pending (job) = %+v, want not complete", d)
	}
	resumed := wt
	resumed.AutoResumedBy = "j2"
	if d := s.todoCompletion(resumed, jobMode, false); d.complete {
		t.Fatalf("job with a continuation (job mode) = %+v, want not complete", d)
	}
	if d := s.todoCompletion(wt, jobMode, false); !d.complete {
		t.Fatalf("job mode worktree job = %+v, want complete", d)
	}
}

// TestMergeModeContinuedJobDoesNotCompleteOnMergedDelivery: a job whose row names a
// continuation does not complete its todo through its own merged delivery (F1).
func TestMergeModeContinuedJobDoesNotCompleteOnMergedDelivery(t *testing.T) {
	s, j, _ := mergePlanWorktreeJob(t, "", true)
	cur, _ := s.Get(j.ID)
	cur.AutoResumedBy = "job-continuation"
	if err := s.persist(cur); err != nil {
		t.Fatal(err)
	}
	if err := s.recordDelivery(j.ID, DeliveryResult{Mode: "merge", State: delivery.StateMerged, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if td := getTodo(t, s, "todo-m"); td.Status != jobstore.TodoDoing {
		t.Fatalf("todo = %q, want doing while the continuation runs", td.Status)
	}
}

// TestUncommittedResumeDoesNotCompleteTodo: the uncommitted-work guard continues the job,
// so the todo is not done at the first job's end (both plan modes); the continuation's
// own ending decides.
func TestUncommittedResumeDoesNotCompleteTodo(t *testing.T) {
	repo := t.TempDir()
	uncommittedGit(t, repo, "init")
	uncommittedWrite(t, repo, "base.txt", "base")
	uncommittedGit(t, repo, "add", "base.txt")
	uncommittedGit(t, repo, "commit", "-m", "base")
	run := &uncommittedRunner{repo: repo}
	s := uncommittedService(t, repo, "resume", run)
	seedPlanTodo(t, s, "plan-r", "todo-r")
	first := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "fake", Runner: "local", Prompt: "work", Cwd: ".", TimeoutSec: 30, TodoID: "todo-r"})
	var source JobResult
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		source, _ = s.Get(first.ID)
		if source.AutoResumedBy != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if source.AutoResumedBy == "" {
		t.Fatal("resume did not submit a continuation")
	}
	if _, ok := s.Wait(source.AutoResumedBy); !ok {
		t.Fatal("continuation vanished")
	}
	s.WaitTerminalHooks(10 * time.Second)
	// The continuation is still dirty and parks in needs_review: the todo is NOT done.
	if td := getTodo(t, s, "todo-r"); td.Status == jobstore.TodoDone {
		t.Fatalf("todo = %q, want not done while the work awaits review", td.Status)
	}
}

// TestRemoveWorktreeNotesDiscardedMergeTodo: `worktree rm` of a pending merge-mode todo's
// worktree keeps the todo doing and records that it was discarded unmerged.
func TestRemoveWorktreeNotesDiscardedMergeTodo(t *testing.T) {
	s, j, _ := mergePlanWorktreeJob(t, "", true)
	if _, err := s.RemoveWorktree(j.ID, true, true); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	td := getTodo(t, s, "todo-m")
	if td.Status != jobstore.TodoDoing || !strings.Contains(td.Note, "已丢弃") {
		t.Fatalf("todo = %q %q, want doing + discarded note", td.Status, td.Note)
	}
}

// TestTodoCompleteFailedEvent: when the todo write fails after the deliverable landed,
// the job carries a todo.complete_failed event (the attention card reads it).
func TestTodoCompleteFailedEvent(t *testing.T) {
	s, j, _ := mergePlanWorktreeJob(t, "", true)
	s.todoCompleteFailed(j.ID, "todo-m", errTest("disk full"))
	evs, err := s.ListJobEvents(j.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type == EventTodoCompleteFailed && strings.Contains(ev.Detail, "todo-m") && strings.Contains(ev.Detail, "disk full") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s event among %d events", EventTodoCompleteFailed, len(evs))
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
