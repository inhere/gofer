package today

import (
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// Blocking score rules (design §2.2). Fixed and explainable: every point comes from one
// line of Blocks.Text.
const (
	scorePerTodo      = 3 // each plan todo waiting on this card (direct + transitive)
	scoreAgentBase    = 2 // an agent process / session lock / concurrency slot is held
	scoreAgentPer10m  = 1 // ... plus one per 10 minutes held
	scoreAgentCap     = 8
	scoreLock         = 1 // a worktree / directory lock is held
	scoreWorkOnlineMe = 2 // a needs_me work item with an online session
	// scoreApprovalBase is a job held for approval (gofer-5wvc): it cannot start at all
	// until a person approves it. Without it the card scores 0 and sinks behind every
	// other 0-point card — same score means "longest wait first", so the NEWEST hold
	// ended up at the very bottom of 「待决策」 while it was the one thing blocking a job.
	scoreApprovalBase = 2
)

// blockSet accumulates a card's blocking sources.
type blockSet struct {
	lang         i18n.Lang
	score, items int
	text         []string
}

func (b *blockSet) add(score, items int, text string) {
	b.score += score
	b.items += items
	if text != "" {
		b.text = append(b.text, text)
	}
}

// agent scores a held agent session: 2 + 1 per 10 minutes, capped at 8.
func (b *blockSet) agent(agent string, heldSec int64) {
	if heldSec < 0 {
		heldSec = 0
	}
	score := scoreAgentBase + int(heldSec/600)*scoreAgentPer10m
	if score > scoreAgentCap {
		score = scoreAgentCap
	}
	b.add(score, 1, strings.TrimSpace(i18n.T(b.lang, "today.blocks.holding", "agent", agent, "what", i18n.T(b.lang, "today.blocks.session"), "duration", durationText(b.lang, heldSec))))
}

// locks scores the worktree / directory lock a running job holds.
func (b *blockSet) locks(rec jobstore.JobRecord) {
	switch {
	case rec.WorktreePath != "":
		b.add(scoreLock, 1, i18n.T(b.lang, "today.blocks.lock_worktree"))
	case rec.DirExclusive:
		b.add(scoreLock, 1, i18n.T(b.lang, "today.blocks.lock_dir"))
	}
}

func (b *blockSet) result() Blocks {
	text := ""
	for i, part := range b.text {
		if i == 0 {
			text = part
			continue
		}
		text = i18n.T(b.lang, "today.blocks.join", "a", text, "b", part)
	}
	return Blocks{Score: b.score, Items: b.items, Text: text}
}

// addSuccessors scores the plan todos that (directly or transitively) wait on todoID
// and are not finished.
func (b *builder) addSuccessors(blk *blockSet, planID, todoID string) error {
	if planID == "" || todoID == "" {
		return nil
	}
	todos, err := b.planTodos(planID)
	if err != nil {
		return err
	}
	n := countSuccessors(todos, todoID)
	if n > 0 {
		blk.add(n*scorePerTodo, n, i18n.T(b.lang, "today.blocks.plan_after", "plan", b.planName(planID), "n", n))
	}
	return nil
}

// addWaitingTodos scores a plan question: the plan's todos that have not started.
func (b *builder) addWaitingTodos(blk *blockSet, plan jobstore.Plan) error {
	todos, err := b.planTodos(plan.PlanID)
	if err != nil {
		return err
	}
	n := 0
	for _, t := range todos {
		if t.Status == jobstore.TodoPending || t.Status == jobstore.TodoReady {
			n++
		}
	}
	if n > 0 {
		blk.add(n*scorePerTodo, n, i18n.T(b.lang, "today.blocks.plan_pending", "plan", firstNonEmpty(plan.Title, plan.PlanID), "n", n))
	}
	return nil
}

func (b *builder) planName(planID string) string {
	if p, ok, err := b.store().GetPlan(planID); err == nil && ok && p.Title != "" {
		return p.Title
	}
	return planID
}

// countSuccessors walks the reverse `after` graph from todoID and counts the reachable
// todos that are neither done nor skipped.
func countSuccessors(todos []jobstore.PlanTodo, todoID string) int {
	dependents := make(map[string][]string, len(todos))
	status := make(map[string]string, len(todos))
	for _, t := range todos {
		status[t.TodoID] = t.Status
		for _, a := range t.After {
			dependents[a] = append(dependents[a], t.TodoID)
		}
	}
	seen := map[string]bool{todoID: true}
	queue := []string{todoID}
	n := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range dependents[cur] {
			if seen[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, next)
			if st := status[next]; st != jobstore.TodoDone && st != jobstore.TodoSkipped {
				n++
			}
		}
	}
	return n
}

// urgencyOf: a deadline within the hour is 「会超时」; anything holding something up is
// 「卡住别人」; the rest 「可稍后」.
func urgencyOf(c Card, now int64) string {
	if c.ExpiresAt > 0 && c.ExpiresAt-now <= nowWindowSec {
		return UrgencyNow
	}
	if c.Blocks.Score > 0 {
		return UrgencyBlocking
	}
	return UrgencyNormal
}

// SortCards orders the queue: urgency=now first (soonest deadline first), then score
// descending, then the longest wait first; the key breaks remaining ties.
func SortCards(cards []Card) {
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := cards[i], cards[j]
		an, bn := a.Urgency == UrgencyNow, b.Urgency == UrgencyNow
		if an != bn {
			return an
		}
		if an && a.ExpiresAt != b.ExpiresAt {
			return a.ExpiresAt < b.ExpiresAt
		}
		if a.Blocks.Score != b.Blocks.Score {
			return a.Blocks.Score > b.Blocks.Score
		}
		if a.WaitingSince != b.WaitingSince {
			return a.WaitingSince < b.WaitingSince
		}
		return a.Key < b.Key
	})
}

func durationText(lang i18n.Lang, sec int64) string {
	switch {
	case sec < 60:
		return ""
	case sec < 3600:
		return i18n.T(lang, "today.duration.minutes", "n", sec/60)
	case sec < 86400:
		return i18n.T(lang, "today.duration.hours", "n", sec/3600)
	default:
		return i18n.T(lang, "today.duration.days", "n", sec/86400)
	}
}
