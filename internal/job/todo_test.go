package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// seedPlanTodo inserts a plan plus one todo of it into the service's store — the
// checklist a `job run --todo` is submitted against (SUP-01 C).
func seedPlanTodo(t *testing.T, s *Service, planID, todoID string) {
	t.Helper()
	if err := s.Meta().InsertPlan(jobstore.Plan{PlanID: planID, Title: planID}); err != nil {
		t.Fatalf("insert plan %s: %v", planID, err)
	}
	if err := s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: todoID, PlanID: planID, Title: todoID}); err != nil {
		t.Fatalf("insert todo %s: %v", todoID, err)
	}
}

// getTodo reads a seeded todo.
func getTodo(t *testing.T, s *Service, todoID string) jobstore.PlanTodo {
	t.Helper()
	td, ok, err := s.Meta().GetTodo(todoID)
	if err != nil || !ok {
		t.Fatalf("GetTodo(%s): ok=%v err=%v", todoID, ok, err)
	}
	return td
}

// TestSubmitWithTodoSetsDoingAndPlan: a job submitted for a todo resolves its
// plan FROM the todo when the caller gave none, refuses a contradicting plan and
// an unknown todo, marks the todo `doing` and binds it to the new job; when the
// job finishes done the todo follows — done, with the commit list in its note.
func TestSubmitWithTodoSetsDoingAndPlan(t *testing.T) {
	s := newTestService(t, t.TempDir())
	seedPlanTodo(t, s, "plan-c", "todo-c")
	seedPlanTodo(t, s, "plan-other", "todo-other")

	// An unknown todo is a client error, not a job that silently links nothing.
	if _, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, TodoID: "todo-nope",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown todo_id must be ErrInvalidRequest, got %v", err)
	}
	// An explicit plan that contradicts the todo's own plan is refused too: one of
	// the two is wrong, and guessing would link the outcome to the wrong plan.
	if _, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", PlanID: "plan-other",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, TodoID: "todo-c",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("plan_id conflicting with the todo's plan must be ErrInvalidRequest, got %v", err)
	}
	if td := getTodo(t, s, "todo-c"); td.Status != jobstore.TodoPending || td.JobID != "" {
		t.Fatalf("a refused submit must not touch the checklist: %+v", td)
	}

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, TodoID: "todo-c",
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.TodoID != "todo-c" {
		t.Fatalf("todo_id = %q, want todo-c", final.TodoID)
	}
	if final.PlanID != "plan-c" {
		t.Fatalf("plan_id = %q, want the todo's plan plan-c", final.PlanID)
	}

	td := getTodo(t, s, "todo-c")
	if td.Status != jobstore.TodoDone {
		t.Fatalf("todo status = %q, want done after the job finished", td.Status)
	}
	if td.JobID != final.ID {
		t.Fatalf("todo job_id = %q, want the job that carried it %q", td.JobID, final.ID)
	}
	// No repository here, so there is nothing to list — the note says so rather
	// than leaving the reader wondering whether the capture failed.
	if !strings.Contains(td.Note, final.ID+" ✓ no commits") {
		t.Fatalf("todo note = %q, want the done line with no commits", td.Note)
	}

	// A second run re-points the todo (most recent job) without dragging it back
	// from done — the checklist is the human's, and a re-run only appends.
	again := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, TodoID: "todo-c",
	})
	td = getTodo(t, s, "todo-c")
	if td.Status != jobstore.TodoDone {
		t.Fatalf("a re-run must not move a done todo back: %+v", td)
	}
	if td.JobID != again.ID {
		t.Fatalf("todo job_id = %q, want the most recent job %q", td.JobID, again.ID)
	}
	if lines := strings.Split(td.Note, "\n"); len(lines) != 2 {
		t.Fatalf("each run appends one line, got %d: %q", len(lines), td.Note)
	}
}

// TestTodoNoteOnFailure: a job that fails leaves its todo where it was (`doing`)
// and appends WHY — the checklist is where a human reads what happened without
// opening the job.
func TestTodoNoteOnFailure(t *testing.T) {
	s := newTestService(t, t.TempDir())
	seedPlanTodo(t, s, "plan-f", "todo-f")

	// A command that cannot even start: the job fails with an ERROR string (a plain
	// non-zero exit records only an exit code), which is what the note must quote.
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"gofer-definitely-not-a-binary"}, Cwd: ".", TimeoutSec: 30, TodoID: "todo-f",
	})
	if final.Status != StatusFailed {
		t.Fatalf("status = %s (err=%s), want failed", final.Status, final.Error)
	}

	td := getTodo(t, s, "todo-f")
	if td.Status != jobstore.TodoDoing {
		t.Fatalf("todo status = %q, want doing (a failed run does not close the item)", td.Status)
	}
	if !strings.HasPrefix(td.Note, final.ID+" ✗ "+final.Status+":") {
		t.Fatalf("todo note = %q, want a one-line failure entry for %s", td.Note, final.ID)
	}
	if final.Error == "" || !strings.Contains(td.Note, strings.SplitN(final.Error, "\n", 2)[0]) {
		t.Fatalf("todo note = %q, want the job's error in it (err=%q)", td.Note, final.Error)
	}
	if len([]rune(td.Note)) > maxTodoNoteErrorRunes+64 {
		t.Fatalf("the failure line must stay short, got %d runes: %q", len([]rune(td.Note)), td.Note)
	}
}

// TestTodoNeedsReviewThenAccept: a reviewed job stops at needs_review, which the
// checklist records as 待验收 WITHOUT closing the item; accepting it then closes
// the item exactly like a normally-finished job.
func TestTodoNeedsReviewThenAccept(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	seedPlanTodo(t, s, "plan-r", "todo-r")

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
		Review: true, SessionID: "sess-review", TodoID: "todo-r",
	})
	if final.Status != StatusNeedsReview {
		t.Fatalf("status = %s (err=%s), want needs_review", final.Status, final.Error)
	}
	td := getTodo(t, s, "todo-r")
	if td.Status != jobstore.TodoDoing {
		t.Fatalf("todo status = %q, want doing while a delivery awaits review", td.Status)
	}
	if !strings.Contains(td.Note, final.ID+" 待验收") {
		t.Fatalf("todo note = %q, want the 待验收 entry", td.Note)
	}

	if _, err := s.AcceptJob(final.ID, "alice", "lgtm"); err != nil {
		t.Fatalf("AcceptJob: %v", err)
	}
	td = getTodo(t, s, "todo-r")
	if td.Status != jobstore.TodoDone {
		t.Fatalf("todo status = %q, want done after acceptance", td.Status)
	}
	if !strings.Contains(td.Note, final.ID+" ✓ ") {
		t.Fatalf("todo note = %q, want the done line appended after acceptance", td.Note)
	}
	if !strings.Contains(td.Note, "待验收") {
		t.Fatalf("the review entry must stay in the note, got %q", td.Note)
	}
}

// TestResumeInheritsTodo: a continuation of a todo's job keeps the linkage, so a
// multi-round chain appends its rounds to the SAME checklist item instead of
// orphaning them.
func TestResumeInheritsTodo(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0})
	seedPlanTodo(t, s, "plan-res", "todo-res")

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "first round", Cwd: ".", TimeoutSec: 30,
		SessionID: "sess-res", TodoID: "todo-res",
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if td := getTodo(t, s, "todo-res"); td.Status != jobstore.TodoDone {
		t.Fatalf("todo status = %q, want done", td.Status)
	}

	cont, err := s.ResumeJob(final.ID, "second round", "", "alice")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if cont.TodoID != "todo-res" || cont.PlanID != "plan-res" {
		t.Fatalf("continuation lost the todo linkage: todo_id=%q plan_id=%q", cont.TodoID, cont.PlanID)
	}
	td := getTodo(t, s, "todo-res")
	if td.JobID != cont.ID {
		t.Fatalf("todo job_id = %q, want the continuation %q", td.JobID, cont.ID)
	}
	if td.Status != jobstore.TodoDone {
		t.Fatalf("a continuation of a closed item must not reopen it: %+v", td)
	}
	// The continuation runs asynchronously: let it reach a terminal state before the
	// test returns, or it is still writing into the TempDir when the test framework
	// removes it ("directory not empty" / "file in use" on Windows).
	waitForStatus(t, s, cont.ID, StatusDone, 10*time.Second)
}
