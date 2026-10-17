package today

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestApprovalCardOutranksZeroScoreCards (gofer-5wvc): a held job scores the
// 「等待批准才能开工」 base, so it sorts above the 0-point cards. Before it, an approval
// card scored 0 unless a plan waited on it, and SortCards breaks a tie by the longest
// wait first — so a job that had just parked landed at the very BOTTOM of 「待决策」.
func TestApprovalCardOutranksZeroScoreCards(t *testing.T) {
	svc, st := newTestService(t)

	// A hold about to lapse: urgency=now, so it still leads the queue.
	putJob(t, st, jobstore.JobRecord{ID: "j-soon", ProjectKey: "p1", Agent: "exec", Status: job.StatusAwaitingApproval,
		StartedAt: ago(5), HoldExpiresAt: testNow.Unix() + 600})
	// A hold with a day left: no deadline urgency, the base score only.
	putJob(t, st, jobstore.JobRecord{ID: "j-hold", ProjectKey: "p1", Agent: "exec", Status: job.StatusAwaitingApproval,
		StartedAt: ago(1), HoldExpiresAt: testNow.Unix() + 86400})
	// A needs_review job holding a worktree: 1 point (the lock), so it keeps its place
	// between the approval base and the 0-point cards.
	putJob(t, st, jobstore.JobRecord{ID: "j-rev", ProjectKey: "p1", Agent: "codex", Status: job.StatusNeedsReview,
		StartedAt: ago(30), EndedAt: ago(20), UpdatedAt: ago(20), WorktreePath: "/wt/j-rev"})
	// Two plan-less decisions: 0 points, oldest wait first (unchanged by this fix). A
	// timeout far out keeps them 「可稍后」 — a deadline inside the hour would make them
	// urgency=now, which is a different tier.
	for id, asked := range map[string]int64{"d-old": ago(120), "d-new": ago(10)} {
		assert.NoErr(t, st.InsertDecision(&jobstore.PlanDecision{ID: id, Title: id, Question: "q", AskedAt: asked, TimeoutSec: 14400}))
	}

	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)

	assert.Eq(t, []string{
		"approval:j-soon", // urgency now (deadline inside the hour) still wins
		"approval:j-hold", // score 2: 等待批准才能开工
		"review:j-rev",    // score 1: the worktree lock
		"decision:d-old",  // score 0: waited longest
		"decision:d-new",
	}, keys(cards))

	hold, ok := find(cards, "approval:j-hold")
	assert.True(t, ok)
	assert.Eq(t, scoreApprovalBase, hold.Blocks.Score)
	assert.Eq(t, 1, hold.Blocks.Items)
	assert.Eq(t, "等待批准才能开工", hold.Blocks.Text)
	assert.Eq(t, UrgencyBlocking, hold.Urgency)
}

// TestApprovalBaseAddsToThePlanScore: the base is added to the plan successors a hold
// already scores, so a hold with a plan waiting on it still outranks a bare one.
func TestApprovalBaseAddsToThePlanScore(t *testing.T) {
	svc, st := newTestService(t)
	assert.NoErr(t, st.InsertPlan(jobstore.Plan{PlanID: "plan-release-01", Title: "release", ProjectKey: "p1", Status: jobstore.PlanOpen}))
	for _, todo := range []jobstore.PlanTodo{
		{TodoID: "t1", PlanID: "plan-release-01", Title: "tag", Status: jobstore.TodoDoing},
		{TodoID: "t2", PlanID: "plan-release-01", Title: "publish", After: []string{"t1"}},
	} {
		assert.NoErr(t, st.InsertTodo(todo))
	}
	putJob(t, st, jobstore.JobRecord{ID: "j-plan", ProjectKey: "p1", Agent: "exec", Status: job.StatusAwaitingApproval,
		StartedAt: ago(1), HoldExpiresAt: testNow.Unix() + 86400, PlanID: "plan-release-01", TodoID: "t1"})

	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	c, ok := find(cards, "approval:j-plan")
	assert.True(t, ok)
	assert.Eq(t, scoreApprovalBase+scorePerTodo, c.Blocks.Score)
	assert.Eq(t, 2, c.Blocks.Items)
	assert.Eq(t, "等待批准才能开工；plan release 后面 1 项在等", c.Blocks.Text)
}
