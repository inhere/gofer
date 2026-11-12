package job

import (
	"log/slog"

	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// Todo completion timing (batch 1, design §1). A plan's `complete_on` decides when a
// todo bound to a finished job counts as done:
//
//   - `job` (default): when the job is done — unchanged;
//   - `merge`: for a worktree job, when its deliverable has landed (a `merged` delivery
//     or a `worktree merge`), so the chain does not start the next todo on top of work
//     nobody has merged yet.
//
// Both modes share one correction: a job that is about to be continued (the
// uncommitted-work guard decided "resume", or the row already names its continuation)
// does not complete its todo — the continuation's own ending does.
//
// This file is the only place the decision lives; linkTodoOutcome (job ended / accepted),
// recordDelivery (delivery landed) and markMergeTodoDone (worktree merge) call into it.

// EventTodoCompleteFailed is recorded on the job when its todo could not be marked done
// after the deliverable had landed (the merge succeeded, the todo write did not). The
// attention surface reads it: the work is merged, only the checklist is stale.
const EventTodoCompleteFailed = "todo.complete_failed"

// todoCompletionDecision is what todoCompletion decides for a job that ended done.
type todoCompletionDecision struct {
	// complete: mark the todo done and advance the plan.
	complete bool
	// note is an extra line for the todo's note: for a complete decision it follows the
	// usual done line, for a deferred one it is the only line ("" = nothing to say).
	note string
}

// todoCompletion decides, for a job that ended `done`, whether its todo is done now.
// resumePending is the guard's "resume" decision (the continuation is being submitted).
func (s *Service) todoCompletion(snap JobResult, plan jobstore.Plan, resumePending bool) todoCompletionDecision {
	// A job that is being continued does not finish its todo: the continuation does.
	if resumePending || snap.AutoResumedBy != "" {
		return todoCompletionDecision{}
	}
	if !plan.MergeCompletion() {
		return todoCompletionDecision{complete: true}
	}
	lang := s.notifyLang(snap.ProjectKey)
	// Not a worktree job (a `ref:` temporary checkout included): nothing to merge.
	if snap.WorktreeBranch == "" {
		return todoCompletionDecision{complete: true}
	}
	// A worker's worktree: gofer cannot merge it, so waiting would wait forever.
	cfg := s.config()
	if snap.Runner != "" && IsRemoteRunner(cfg, snap.Runner) {
		return todoCompletionDecision{complete: true, note: i18n.T(lang, "job.todo_note.remote_job", "job", snap.ID)}
	}
	// Nothing to deliver.
	if snap.CommitsAhead <= 0 {
		return todoCompletionDecision{complete: true, note: i18n.T(lang, "job.todo_note.no_changes", "job", snap.ID)}
	}
	switch EffectiveDeliveryMode(cfg, requestOfJob(snap)) {
	case delivery.ModeMerge:
		if snap.Delivery == nil {
			// The automatic delivery runs right after this hook: recordDelivery completes
			// the todo when (and only when) it lands as merged.
			return todoCompletionDecision{}
		}
		if snap.Delivery.State == delivery.StateMerged {
			return todoCompletionDecision{complete: true}
		}
		return todoCompletionDecision{note: s.deliveryPendingNote(snap, *snap.Delivery)}
	default: // none / push / pr: a human (or a later batch's PR follow-up) merges it
		return todoCompletionDecision{note: i18n.T(lang, "job.todo_note.awaiting_merge", "job", snap.ID, "branch", snap.WorktreeBranch)}
	}
}

// deliveryPendingNote is the todo line for a delivery that landed somewhere other than
// the main branch (or failed): the reason the todo stays doing.
func (s *Service) deliveryPendingNote(snap JobResult, res DeliveryResult) string {
	lang := s.notifyLang(snap.ProjectKey)
	switch res.State {
	case delivery.StatePushed:
		return i18n.T(lang, "job.todo_note.delivery_pushed", "job", snap.ID, "branch", firstNonEmptyString(res.Branch, snap.WorktreeBranch))
	case delivery.StatePROpened:
		return i18n.T(lang, "job.todo_note.delivery_pr_opened", "job", snap.ID, "url", res.PRURL)
	case delivery.StateFailed:
		return i18n.T(lang, "job.todo_note.delivery_failed", "job", snap.ID, "error", firstRunes(res.Error, maxTodoNoteErrorRunes))
	}
	return ""
}

// todoDeliveryRecorded is recordDelivery's hook: a delivery of the job's branch has
// been stored. For a merge-mode plan, a `merged` delivery completes the todo the job
// carries (idempotent: a todo already closed, or already bound to a later job, is left
// alone); the other final states say why it stays doing. Best-effort, like every todo write.
func (s *Service) todoDeliveryRecorded(snap JobResult, res DeliveryResult) {
	if snap.TodoID == "" || s.meta == nil || res.State == delivery.StatePending || res.State == delivery.StateSkipped {
		return
	}
	todo, ok, err := s.meta.GetTodo(snap.TodoID)
	if err != nil || !ok || todo.Status == jobstore.TodoDone || todo.Status == jobstore.TodoSkipped {
		return
	}
	// The todo belongs to a later run now (a continuation): that run's ending counts.
	if todo.JobID != "" && todo.JobID != snap.ID {
		return
	}
	plan, ok, err := s.meta.GetPlan(todo.PlanID)
	if err != nil || !ok || !plan.MergeCompletion() {
		return
	}
	if snap.AutoResumedBy != "" {
		return
	}
	if res.State != delivery.StateMerged {
		if note := s.deliveryPendingNote(snap, res); note != "" {
			s.appendTodoNote(snap, note)
		}
		return
	}
	if _, err := s.meta.UpdateTodoStatus(snap.TodoID, jobstore.TodoDone, nil); err != nil {
		s.todoCompleteFailed(snap.ID, snap.TodoID, err)
		return
	}
	s.appendTodoNote(snap, i18n.T(s.notifyLang(snap.ProjectKey), "job.todo_note.delivery_merged", "job", snap.ID, "branch", snap.WorktreeBranch))
	s.PlanTodoChanged(snap.TodoID, jobstore.TodoDone, snap.CallerID)
}

// todoCompleteFailed logs and records that the deliverable landed but the todo write
// failed (R4): the attention surface offers "mark it done" from the event.
func (s *Service) todoCompleteFailed(jobID, todoID string, err error) {
	slog.Warn("complete todo after delivery", "todo_id", todoID, "job_id", jobID, "err", err)
	s.recordEvent(jobID, EventTodoCompleteFailed, map[string]any{"todo_id": todoID, "job_id": jobID, "err": err.Error()})
}

// todoMergeCompletes reports whether a `worktree merge` of this job completes its todo
// without --todo-done: the todo belongs to a merge-mode plan, where merging IS the
// completion condition.
func (s *Service) todoMergeCompletes(todoID string) bool {
	if todoID == "" || s.meta == nil {
		return false
	}
	todo, ok, err := s.meta.GetTodo(todoID)
	if err != nil || !ok {
		return false
	}
	plan, ok, err := s.meta.GetPlan(todo.PlanID)
	return err == nil && ok && plan.MergeCompletion()
}

// todoWorktreeDiscarded notes that a pending todo's worktree was removed without a
// merge (`worktree rm`): the todo stays doing, so the attention card keeps it visible
// with the right action (design §1 F7).
func (s *Service) todoWorktreeDiscarded(res JobResult) {
	if res.TodoID == "" || !s.todoMergeCompletes(res.TodoID) {
		return
	}
	todo, ok, err := s.meta.GetTodo(res.TodoID)
	if err != nil || !ok || todo.Status == jobstore.TodoDone || todo.Status == jobstore.TodoSkipped {
		return
	}
	// Only the run the todo points at: an older job's worktree does not speak for it.
	if todo.JobID != "" && todo.JobID != res.ID {
		return
	}
	s.appendTodoNote(res, i18n.T(s.notifyLang(res.ProjectKey), "job.todo_note.discarded", "job", res.ID))
}
