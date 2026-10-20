package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// newTodoWorktreeJob submits a finished --worktree job that carries todo-h (plan-h) over
// HTTP, resets the todo to doing (the job's own completion already closed it, so the
// merge must be what finishes it) and leaves one commit on the job's branch.
func newTodoWorktreeJob(t *testing.T, s *Server, file string) job.JobResult {
	t.Helper()
	if err := s.jobs.Meta().InsertPlan(jobstore.Plan{PlanID: "plan-h", Title: "plan-h"}); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	if err := s.jobs.Meta().InsertTodo(jobstore.PlanTodo{TodoID: "todo-h", PlanID: "plan-h", Title: "todo-h"}); err != nil {
		t.Fatalf("insert todo: %v", err)
	}
	resp := do(t, s, http.MethodPost, "/v1/jobs?wait=1", testToken, map[string]any{
		"project_key": "repo", "agent": "exec", "runner": "local",
		"cmd": []string{"go", "version"}, "timeout_sec": 60, "worktree": true, "todo_id": "todo-h",
	})
	var j job.JobResult
	decode(t, resp, &j)
	if j.WorktreePath == "" || j.TodoID != "todo-h" {
		t.Fatalf("job = worktree %q todo %q, want a worktree + todo-h", j.WorktreePath, j.TodoID)
	}
	if _, ok := s.jobs.Wait(j.ID); !ok {
		t.Fatalf("job %s not found", j.ID)
	}
	if ok, err := s.jobs.Meta().UpdateTodoStatus("todo-h", jobstore.TodoDoing, nil); err != nil || !ok {
		t.Fatalf("reset todo-h to doing: ok=%v err=%v", ok, err)
	}
	commitIn(t, j.WorktreePath, file, file+"\n", "deliver "+file)
	return j
}

func planPausedEvents(t *testing.T, s *Server) int {
	t.Helper()
	evs, err := s.jobs.ListJobEvents(job.PlanEventScope("plan-h"), 0)
	if err != nil {
		t.Fatalf("list plan events: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == job.EventPlanAdvancePaused {
			n++
		}
	}
	return n
}

// TestWorktreeMergeTodoDone: the body's todo_done / note reach the job layer — the todo
// turns done with the default `<branch> 合入 <sha>` note or the given one, the answer
// reports it, and the plan chain is told.
func TestWorktreeMergeTodoDone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, note string }{
		{"default note", ""},
		{"explicit note", "landed via http"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, repo := newWorktreeTestServer(t)
			j := newTodoWorktreeJob(t, s, "td.txt")
			if _, err := s.jobs.PausePlan("plan-h"); err != nil {
				t.Fatalf("PausePlan: %v", err)
			}
			before := planPausedEvents(t, s)

			body := map[string]any{"todo_done": true}
			if tc.note != "" {
				body["note"] = tc.note
			}
			resp := do(t, s, http.MethodPost, "/v1/jobs/"+j.ID+"/worktree/merge", testToken, body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("merge status=%d, want 200", resp.StatusCode)
			}
			var out job.MergeResult
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decode merge answer: %v", err)
			}
			resp.Body.Close()
			want := tc.note
			if want == "" {
				want = j.WorktreeBranch + " 合入 " + gitTestRun(t, repo, "rev-parse", "--short", "HEAD")
			}
			if !out.Merged || !out.TodoDone || out.TodoID != "todo-h" || out.TodoNote != want || len(out.Warnings) != 0 {
				t.Fatalf("merge answer = %+v, want merged + todo-h done with note %q", out, want)
			}
			td, ok, err := s.jobs.Meta().GetTodo("todo-h")
			if err != nil || !ok || td.Status != jobstore.TodoDone || td.Note != want {
				t.Fatalf("todo = %+v (ok=%v err=%v), want done + note %q", td, ok, err, want)
			}
			if after := planPausedEvents(t, s); after != before+1 {
				t.Fatalf("plan.advance_paused events %d -> %d, want one more", before, after)
			}
		})
	}
}

// TestWorktreeMergeConflictKeepsTodo: a 409 conflict leaves the todo, comments and the
// checkout alone even when todo_done / comment / cleanup were requested.
func TestWorktreeMergeConflictKeepsTodo(t *testing.T) {
	t.Parallel()
	s, repo := newWorktreeTestServer(t)
	j := newTodoWorktreeJob(t, s, "other.txt")
	commitIn(t, j.WorktreePath, "README.md", "branch side\n", "branch readme")
	commitIn(t, repo, "README.md", "main side\n", "main readme")
	todoBefore, _, _ := s.jobs.Meta().GetTodo("todo-h")
	if _, err := s.jobs.PausePlan("plan-h"); err != nil {
		t.Fatalf("PausePlan: %v", err)
	}
	before := planPausedEvents(t, s)

	resp := do(t, s, http.MethodPost, "/v1/jobs/"+j.ID+"/worktree/merge", testToken, map[string]any{
		"todo_done": true, "note": "must not land", "comment": "merged", "cleanup": true,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("merge status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	td, _, _ := s.jobs.Meta().GetTodo("todo-h")
	if td.Status != jobstore.TodoDoing || td.Note != todoBefore.Note {
		t.Fatalf("todo = status %q note %q, want unchanged doing + %q", td.Status, td.Note, todoBefore.Note)
	}
	if comments, err := s.jobs.ListComments(jobstore.CommentScopeJob, j.ID); err != nil || len(comments) != 0 {
		t.Fatalf("comments = %+v (err=%v), want none", comments, err)
	}
	if _, err := os.Stat(filepath.Join(j.WorktreePath, "other.txt")); err != nil {
		t.Fatalf("conflicting merge removed the worktree: %v", err)
	}
	if out := gitTestRun(t, repo, "branch", "--list", j.WorktreeBranch); !strings.Contains(out, j.WorktreeBranch) {
		t.Fatalf("conflicting merge dropped the branch: %q", out)
	}
	if after := planPausedEvents(t, s); after != before {
		t.Fatalf("plan.advance_paused events %d -> %d, want unchanged", before, after)
	}
}
