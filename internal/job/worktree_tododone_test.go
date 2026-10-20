package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// todoWorktreeJob submits a finished --worktree job carrying todo-w (of plan-w) and
// commits one file in its worktree. The job's own completion already closed the todo,
// so the todo is put back to doing: the merge is then the only thing that can finish it.
func todoWorktreeJob(t *testing.T, s *Service, file string) JobResult {
	t.Helper()
	seedPlanTodo(t, s, "plan-w", "todo-w")
	j := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local", TodoID: "todo-w",
		Cmd: []string{"git", "status"}, Cwd: ".", TimeoutSec: 60, Worktree: true,
	})
	if j.TodoID != "todo-w" || j.WorktreePath == "" {
		t.Fatalf("job = todo %q worktree %q, want todo-w + a worktree", j.TodoID, j.WorktreePath)
	}
	if ok, err := s.Meta().UpdateTodoStatus("todo-w", jobstore.TodoDoing, nil); err != nil || !ok {
		t.Fatalf("reset todo-w to doing: ok=%v err=%v", ok, err)
	}
	writeRepoFile(t, j.WorktreePath, file, file+"\n")
	gitOutIn(t, j.WorktreePath, "add", file)
	gitOutIn(t, j.WorktreePath, "-c", "user.email=t@e.x", "-c", "user.name=t", "commit", "-q", "-m", file)
	return j
}

// TestMergeWorktreeTodoDone: --todo-done closes the job's plan todo after a successful
// merge — with the default note `<branch> 合入 <main short sha>` or the operator's — and
// tells the plan chain (a paused plan records plan.advance_paused for the change).
func TestMergeWorktreeTodoDone(t *testing.T) {
	for _, tc := range []struct {
		name, note string
	}{
		{"default note", ""},
		{"explicit note", "landed by the operator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _ := gitRepo(t)
			s := newWorktreeService(t, repo, t.TempDir())
			j := todoWorktreeJob(t, s, "td.txt")
			if _, err := s.PausePlan("plan-w"); err != nil {
				t.Fatalf("PausePlan: %v", err)
			}
			before := countPlanEvent(t, s, "plan-w", EventPlanAdvancePaused)

			res, err := s.MergeWorktree(j.ID, MergeOptions{TodoDone: true, Note: tc.note})
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			want := tc.note
			if want == "" {
				want = j.WorktreeBranch + " 合入 " + gitOutIn(t, repo, "rev-parse", "--short", "HEAD")
			}
			if !res.Merged || !res.TodoDone || res.TodoID != "todo-w" || res.TodoNote != want || len(res.Warnings) != 0 {
				t.Fatalf("result = %+v, want merged + todo-w done with note %q and no warnings", res, want)
			}
			td := getTodo(t, s, "todo-w")
			if td.Status != jobstore.TodoDone || td.Note != want {
				t.Fatalf("todo = status %q note %q, want done + %q", td.Status, td.Note, want)
			}
			if !strings.Contains(td.Note, " 合入 ") && tc.note == "" {
				t.Fatalf("default note %q lacks the 合入 marker", td.Note)
			}
			if after := countPlanEvent(t, s, "plan-w", EventPlanAdvancePaused); after != before+1 {
				t.Fatalf("plan.advance_paused events %d -> %d, want exactly one more (plan notified)", before, after)
			}
		})
	}
}

// TestMergeWorktreeConflictLeavesTodoAlone: a conflicting merge fails before any closing
// step — the todo keeps its status and note, no comment is posted, nothing is cleaned up
// and the plan is not told anything changed.
func TestMergeWorktreeConflictLeavesTodoAlone(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	j := todoWorktreeJob(t, s, "other.txt")
	// Conflict: the same file changed differently in the branch and on main.
	writeRepoFile(t, j.WorktreePath, "README.md", "branch side\n")
	gitOutIn(t, j.WorktreePath, "add", "README.md")
	gitOutIn(t, j.WorktreePath, "-c", "user.email=t@e.x", "-c", "user.name=t", "commit", "-q", "-m", "branch readme")
	writeRepoFile(t, repo, "README.md", "main side\n")
	gitOutIn(t, repo, "add", "README.md")
	gitOutIn(t, repo, "-c", "user.email=t@e.x", "-c", "user.name=t", "commit", "-q", "-m", "main readme")
	if _, err := s.PausePlan("plan-w"); err != nil {
		t.Fatalf("PausePlan: %v", err)
	}
	before := countPlanEvent(t, s, "plan-w", EventPlanAdvancePaused)
	todoBefore := getTodo(t, s, "todo-w")

	res, err := s.MergeWorktree(j.ID, MergeOptions{TodoDone: true, Note: "should not land", Comment: "merged", Cleanup: true})
	if err == nil {
		t.Fatalf("merge succeeded, want a conflict: %+v", res)
	}
	var mce *WorktreeMergeError
	if !errors.As(err, &mce) {
		t.Fatalf("err = %v, want a *WorktreeMergeError", err)
	}
	if res.Merged || res.TodoDone || res.Commented || res.Removed || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v, want no closing step reported", res)
	}
	td := getTodo(t, s, "todo-w")
	if td.Status != jobstore.TodoDoing || td.Note != todoBefore.Note {
		t.Fatalf("todo = status %q note %q, want unchanged doing + %q", td.Status, td.Note, todoBefore.Note)
	}
	if comments, cerr := s.ListComments(jobstore.CommentScopeJob, j.ID); cerr != nil || len(comments) != 0 {
		t.Fatalf("comments = %+v (err=%v), want none", comments, cerr)
	}
	if !isDir(j.WorktreePath) {
		t.Fatal("conflicting merge removed the worktree")
	}
	if out := gitOutIn(t, repo, "branch", "--list", j.WorktreeBranch); !strings.Contains(out, j.WorktreeBranch) {
		t.Fatalf("conflicting merge dropped the branch: %q", out)
	}
	if after := countPlanEvent(t, s, "plan-w", EventPlanAdvancePaused); after != before {
		t.Fatalf("plan.advance_paused events %d -> %d, want unchanged", before, after)
	}
	if st := gitOutIn(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("main left dirty after the conflict: %q", st)
	}
}
