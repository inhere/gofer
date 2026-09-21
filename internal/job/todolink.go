package job

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// Plan-todo linkage (SUP-01 C). A `job run --todo <id>` job and the checklist item
// it carries are two records of one unit of work, and this file is the only place
// they meet:
//
//   - submit resolves the todo's plan and marks the item `doing`;
//   - the terminal hooks (finish, and the accept/reject path) close the item when
//     the job is done/accepted and append ONE line describing the outcome
//     otherwise — the note is the checklist's own log, and the job's logs stay the
//     job's.
//
// Every write is best-effort: a job must never fail to start (or to finish) because
// its checklist could not be updated, so failures are logged and the job's own
// state stays authoritative.

// maxTodoNoteCommits caps how many commits the done line quotes (`+N` says the
// rest), so a 200-commit job does not turn its todo note into a wall of text.
const maxTodoNoteCommits = 8

// maxTodoNoteErrorRunes caps the quoted error of a failed job's line.
const maxTodoNoteErrorRunes = 120

// todoPlanForSubmit resolves the plan a todo-attached submit runs under: the todo's
// own plan when the caller gave none, and a hard error when the two disagree (one of
// them is wrong, and guessing would file the outcome under the wrong plan) or the
// todo does not exist. A foreign todo (the worker re-entering Submit for a job the
// hub owns) is display-only: the todo lives in the hub's store, so nothing here can
// resolve or link it.
func (s *Service) todoPlanForSubmit(req *JobRequest) error {
	if req.TodoID == "" || req.TodoForeign {
		return nil
	}
	todo, ok, err := s.meta.GetTodo(req.TodoID)
	if err != nil {
		return err
	}
	if !ok {
		// An unknown todo on an INTERNAL continuation (ResumeJob / auto-resume of a
		// job rebuilt from the store, where the foreign marker cannot survive): the
		// todo belongs to the store that submitted the original job — a worker's local
		// copy of a hub-owned item — so this side links nothing rather than refusing a
		// continuation it was asked to run. A CLIENT submit with an unknown id is still
		// refused: that is a mistake worth reporting.
		if req.ResumedFrom != "" || req.AutoResumeAttempt > 0 {
			req.TodoForeign = true
			return nil
		}
		return fmt.Errorf("%w: unknown todo_id %q", ErrInvalidRequest, req.TodoID)
	}
	switch {
	case req.PlanID == "":
		req.PlanID = todo.PlanID
	case req.PlanID != todo.PlanID:
		return fmt.Errorf("%w: todo %q belongs to plan %q, not %q",
			ErrInvalidRequest, req.TodoID, todo.PlanID, req.PlanID)
	}
	return nil
}

// linkTodoSubmit ties a freshly submitted job to its todo: the item becomes `doing`
// (unless the human already closed it — a re-run of a done item must not drag it
// back) and points at this job as its most recent run.
func (s *Service) linkTodoSubmit(todoID, jobID string) {
	if todoID == "" {
		return
	}
	todo, ok, err := s.meta.GetTodo(todoID)
	if err != nil {
		slog.Warn("link todo on submit", "todo_id", todoID, "job_id", jobID, "err", err)
		return
	}
	if !ok {
		// A todo this store does not own (the worker's display-only copy).
		slog.Debug("link todo on submit: unknown todo", "todo_id", todoID, "job_id", jobID)
		return
	}
	if todo.Status != jobstore.TodoDone && todo.Status != jobstore.TodoSkipped {
		if _, err := s.meta.UpdateTodoStatus(todoID, jobstore.TodoDoing, nil); err != nil {
			slog.Warn("mark todo doing on submit", "todo_id", todoID, "job_id", jobID, "err", err)
		}
	}
	if _, err := s.meta.SetTodoJob(todoID, jobID); err != nil {
		slog.Warn("bind todo to job", "todo_id", todoID, "job_id", jobID, "err", err)
	}
}

// linkTodoOutcome records a job's terminal outcome on its todo: done closes the
// item and quotes what was delivered, needs_review leaves it doing and says a
// delivery is waiting, and every failure appends why. Runs after the terminal row
// is persisted, on both the finish() and the accept/reject paths.
func (s *Service) linkTodoOutcome(snap JobResult) {
	if snap.TodoID == "" {
		return
	}
	// The worker's own copy of a hub-owned todo: display only, nothing to link.
	todo, ok, err := s.meta.GetTodo(snap.TodoID)
	if err != nil || !ok {
		slog.Debug("link todo on finish: unknown todo", "todo_id", snap.TodoID, "job_id", snap.ID)
		return
	}
	switch snap.Status {
	case StatusDone:
		if _, err := s.meta.UpdateTodoStatus(snap.TodoID, jobstore.TodoDone, nil); err != nil {
			slog.Warn("complete todo on job done", "todo_id", snap.TodoID, "job_id", snap.ID, "err", err)
		}
		s.appendTodoNote(snap, todoDoneLine(snap))
	case StatusNeedsReview:
		s.appendTodoNote(snap, snap.ID+" 待验收")
	default: // failed / timeout / cancelled / rejected
		s.appendTodoNote(snap, todoFailureLine(snap))
	}
	// PLAN-03: a finished item unblocks whatever waited for it. A `needs_review`
	// delivery is NOT finished as far as the chain is concerned (the item stays doing,
	// and its dependents keep waiting for the human's verdict) — and a failure parks
	// the plan instead, in maybeBlockPlan, which runs once the takeover decision is
	// final (see finish).
	if snap.Status == StatusDone {
		s.advancePlan(todo.PlanID, "")
	}
}

func (s *Service) appendTodoNote(snap JobResult, line string) {
	if _, err := s.meta.AppendTodoNote(snap.TodoID, line); err != nil {
		slog.Warn("append todo note", "todo_id", snap.TodoID, "job_id", snap.ID, "err", err)
	}
}

// todoDoneLine is the done entry: the job id, how many commits it produced and
// their (capped) sha + subject list — or `no commits`, which is a fact worth
// stating rather than leaving the reader guessing whether the capture ran.
func todoDoneLine(snap JobResult) string {
	if len(snap.Commits) == 0 {
		return snap.ID + " ✓ no commits"
	}
	parts := make([]string, 0, maxTodoNoteCommits+1)
	for i, c := range snap.Commits {
		if i == maxTodoNoteCommits {
			parts = append(parts, fmt.Sprintf("+%d", len(snap.Commits)-i))
			break
		}
		parts = append(parts, strings.TrimSpace(c.SHA+" "+c.Subject))
	}
	return fmt.Sprintf("%s ✓ %d commits: %s", snap.ID, len(snap.Commits), strings.Join(parts, "; "))
}

// todoFailureLine is the failure entry: the status always, the job's error when it
// has one (a rejection's reason is the review note, which the reviewer sees on the
// job itself) — capped so a stack trace cannot flood the checklist.
func todoFailureLine(snap JobResult) string {
	line := snap.ID + " ✗ " + snap.Status
	if msg := firstRunes(snap.Error, maxTodoNoteErrorRunes); msg != "" {
		line += ": " + msg
	}
	return line
}

// firstRunes trims s and returns at most n runes of it (rune-safe: a truncated
// multi-byte error must not leave a broken tail in the note).
func firstRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
