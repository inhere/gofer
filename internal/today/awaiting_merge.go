package today

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// ---------------------------------------------------------------- awaiting merge

// Subtypes of a KindTodoAwaitingMerge card: why the todo is still doing although its job
// ended done. They are structured facts of the job row (and one event), never read from
// the todo's note.
const (
	// AwaitingMergeWaiting: the worktree branch is not merged yet.
	AwaitingMergeWaiting = "awaiting_merge"
	// AwaitingMergePushed: the delivery pushed the branch / opened a PR; merging is next.
	AwaitingMergePushed = "pushed"
	// AwaitingMergeFailed: the delivery failed.
	AwaitingMergeFailed = "delivery_failed"
	// AwaitingMergeDiscarded: the worktree was removed without a merge.
	AwaitingMergeDiscarded = "discarded"
	// AwaitingMergeUnmarked: the work was merged but the todo write failed (todo.complete_failed).
	AwaitingMergeUnmarked = "merged_unmarked"
)

// awaitingMerge lists the todos of complete_on=merge plans that wait for a person to
// finish them: the job ended done, nothing is running for the todo any more, and the
// deliverable has not landed. Everything is read from the store (job row, delivery
// state, one event) — the read path runs no git and does not read the todo's note.
func (b *builder) awaitingMerge() error {
	plans, err := b.store().ListPlans(jobstore.PlanFilter{Status: jobstore.PlanOpen, Limit: jobstore.PlanListMaxLimit})
	if err != nil {
		return err
	}
	for _, p := range plans {
		if !p.MergeCompletion() {
			continue
		}
		todos, err := b.planTodos(p.PlanID)
		if err != nil {
			return err
		}
		for _, t := range todos {
			if t.Status != jobstore.TodoDoing || t.JobID == "" {
				continue
			}
			rec, err := b.job(t.JobID)
			if err != nil {
				return err
			}
			// The todo's latest bound job decides: a later run (a continuation, a re-run)
			// replaces it, so a running follow-up never leaves a card behind.
			if rec == nil || rec.Status != job.StatusDone || rec.AutoResumedBy != "" || rec.WorktreeBranch == "" {
				continue
			}
			sub, ok, err := b.awaitingMergeSubtype(*rec)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if err := b.addAwaitingMergeCard(p, t, *rec, sub); err != nil {
				return err
			}
		}
	}
	return nil
}

// awaitingMergeSubtype classifies one such job; ok=false means no card (a delivery still
// in flight).
func (b *builder) awaitingMergeSubtype(rec jobstore.JobRecord) (string, bool, error) {
	evs, err := b.store().ListJobEvents(rec.ID, 0)
	if err != nil {
		return "", false, err
	}
	for _, ev := range evs {
		if ev.Type == job.EventTodoCompleteFailed {
			return AwaitingMergeUnmarked, true, nil
		}
	}
	var res delivery.Result
	if rec.DeliveryJSON != "" {
		_ = json.Unmarshal([]byte(rec.DeliveryJSON), &res)
	}
	switch res.State {
	case delivery.StatePending:
		return "", false, nil
	case delivery.StatePushed, delivery.StatePROpened:
		return AwaitingMergePushed, true, nil
	case delivery.StateFailed:
		return AwaitingMergeFailed, true, nil
	case delivery.StateMerged:
		// Landed, and the todo is still doing with no failure event: a state the hooks do
		// not produce; leave it to the plan's own cards rather than guess.
		return "", false, nil
	}
	if rec.WorktreePath == "" {
		return AwaitingMergeDiscarded, true, nil
	}
	return AwaitingMergeWaiting, true, nil
}

func (b *builder) addAwaitingMergeCard(p jobstore.Plan, t jobstore.PlanTodo, rec jobstore.JobRecord, sub string) error {
	waited := rec.EndedAt
	if waited == 0 {
		waited = rec.UpdatedAt
	}
	c := Card{
		Key: KindTodoAwaitingMerge + ":" + t.TodoID, Kind: KindTodoAwaitingMerge, Tag: b.tr("tag.awaiting_merge"),
		Title:      oneLine(firstNonEmpty(p.Title, p.PlanID)+" · "+firstNonEmpty(t.Title, t.TodoID), titleRunes),
		ProjectKey: firstNonEmpty(t.ProjectKey, p.ProjectKey, rec.ProjectKey), Agent: rec.Agent,
		WaitingSince: waited, ActivityAt: rec.UpdatedAt, Subtype: sub,
		Summary: oneLine(b.tr("card.awaiting_merge."+sub, "branch", rec.WorktreeBranch, "job", rec.ID), summaryRunes),
		Refs:    Refs{JobID: rec.ID, PlanID: p.PlanID, TodoID: t.TodoID},
		Actions: []Action{{ID: "open", Label: b.tr("action.open_plan"), Style: "link"}},
	}
	// A todo the person has nothing left to merge for is closed by hand: the plan chain
	// moves on once it is done (or skipped).
	switch sub {
	case AwaitingMergeUnmarked:
		c.Actions = append([]Action{{ID: "todo_done", Label: b.tr("action.todo_done"), Style: "ok"}}, c.Actions...)
	case AwaitingMergeDiscarded:
		c.Actions = append([]Action{{ID: "todo_skip", Label: b.tr("action.todo_skip")}, {ID: "todo_done", Label: b.tr("action.todo_done"), Style: "ok"}}, c.Actions...)
	}
	blk := b.blocks()
	if err := b.addSuccessors(&blk, p.PlanID, t.TodoID); err != nil {
		return err
	}
	c.Blocks = blk.result()
	b.cards = append(b.cards, c)
	return nil
}
