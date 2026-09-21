package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestTodoAfterAutoCmdRoundTrip (PLAN-03): the chain columns of a todo — the items it
// waits for, the auto-advance switch and an exec item's argv — survive a write/read,
// a patch sets and clears each of them, and a row written WITHOUT them reads back as
// the pre-PLAN-03 shape (no dependencies, auto on, no argv).
func TestTodoAfterAutoCmdRoundTrip(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-chain", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))

	// A plain item: no chain fields at all.
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "todo-plain", PlanID: "plan-chain", Title: "plain",
		Status: TodoPending, Auto: true, CreatedAt: 1, UpdatedAt: 1,
	}))
	plain, ok, err := s.GetTodo("todo-plain")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, 0, len(plain.After))
	assert.True(t, plain.Auto)
	assert.Eq(t, 0, len(plain.Cmd))

	// A chained exec item: every field set at creation.
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "todo-exec", PlanID: "plan-chain", Title: "verify",
		Status: TodoPending, Assignee: "exec",
		After: []string{"todo-plain"}, Auto: false,
		Cmd:       []string{"go", "test", "./..."},
		CreatedAt: 2, UpdatedAt: 2,
	}))
	got, ok, err := s.GetTodo("todo-exec")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, 1, len(got.After))
	assert.Eq(t, "todo-plain", got.After[0])
	assert.False(t, got.Auto)
	assert.Eq(t, 3, len(got.Cmd))
	assert.Eq(t, "go", got.Cmd[0])

	// The same fields through a patch, and then cleared again (a non-nil empty value
	// is "clear", the contract every TodoPatch field shares).
	after := []string{"todo-a", "todo-b"}
	auto := true
	cmd := []string{"make", "check"}
	patchOK, err := s.UpdateTodoPatch("todo-plain", TodoPatch{After: &after, Auto: &auto, Cmd: &cmd})
	assert.NoErr(t, err)
	assert.True(t, patchOK)
	patched, _, _ := s.GetTodo("todo-plain")
	assert.Eq(t, 2, len(patched.After))
	assert.True(t, patched.Auto)
	assert.Eq(t, "make", patched.Cmd[0])

	empty := []string{}
	noAuto := false
	clearOK, err := s.UpdateTodoPatch("todo-plain", TodoPatch{After: &empty, Auto: &noAuto, Cmd: &empty})
	assert.NoErr(t, err)
	assert.True(t, clearOK)
	cleared, _, _ := s.GetTodo("todo-plain")
	assert.Eq(t, 0, len(cleared.After))
	assert.False(t, cleared.Auto)
	assert.Eq(t, 0, len(cleared.Cmd))

	// A patch that only names the chain fields is a real update (not "empty"), so the
	// write paths do not drop it as a no-op.
	assert.False(t, TodoPatch{After: &after}.Empty())
	assert.False(t, TodoPatch{Auto: &auto}.Empty())
	assert.False(t, TodoPatch{Cmd: &cmd}.Empty())
}

// TestPlanPausedBlockedRoundTrip (PLAN-03): a plan carries a pause switch and the item
// it parked on; the block and the `blocked` status move together, and clearing releases
// both while leaving a human's own status alone.
func TestPlanPausedBlockedRoundTrip(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{
		PlanID: "plan-state", Title: "state", Status: PlanOpen, ProjectKey: "self",
		CreatedAt: 1, UpdatedAt: 1,
	}))

	p, ok, err := s.GetPlan("plan-state")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.False(t, p.Paused)
	assert.Eq(t, "", p.BlockedTodo)

	assert.NoErr(t, s.SetPlanPaused("plan-state", true))
	p, _, _ = s.GetPlan("plan-state")
	assert.True(t, p.Paused)
	assert.Eq(t, PlanOpen, p.Status, "pausing is not a status change")

	assert.NoErr(t, s.SetPlanBlocked("plan-state", "todo-x"))
	p, _, _ = s.GetPlan("plan-state")
	assert.Eq(t, PlanBlocked, p.Status)
	assert.Eq(t, "todo-x", p.BlockedTodo)

	// Clearing releases the block AND returns the plan to open.
	assert.NoErr(t, s.ClearPlanBlocked("plan-state"))
	p, _, _ = s.GetPlan("plan-state")
	assert.Eq(t, "", p.BlockedTodo)
	assert.Eq(t, PlanOpen, p.Status)

	// Clearing is idempotent and never overwrites a status a human chose.
	assert.NoErr(t, s.ClearPlanBlocked("plan-state"))
	assert.NoErr(t, s.SetPlanPaused("plan-state", false))
	assert.NoErr(t, s.SetPlanStatus("plan-state", PlanActive, -1))
	assert.NoErr(t, s.ClearPlanBlocked("plan-state"))
	p, _, _ = s.GetPlan("plan-state")
	assert.Eq(t, PlanActive, p.Status)
	assert.False(t, p.Paused)

	// A migrated (pre-PLAN-03) row reads back as running and unblocked.
	raw, err := s.db.Exec(`INSERT INTO plans (plan_id, status, created_at, updated_at) VALUES (?,?,?,?)`,
		"plan-old", PlanOpen, 5, 5)
	assert.NoErr(t, err)
	_, _ = raw.RowsAffected()
	old, ok, err := s.GetPlan("plan-old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.False(t, old.Paused)
	assert.Eq(t, "", old.BlockedTodo)
}

// TestSetTodoReadyIfPendingIsConditional (PLAN-03): the chain advance promotes an item
// with ONE conditional statement, so the second caller (a racing terminal outcome, or a
// retry of the same move) is told it did nothing — which is what keeps the queue and its
// event honest.
func TestSetTodoReadyIfPendingIsConditional(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-q", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "todo-q", PlanID: "plan-q", Title: "queued", Status: TodoPending,
		Auto: true, CreatedAt: 1, UpdatedAt: 1,
	}))

	first, err := s.SetTodoReadyIfPending("todo-q")
	assert.NoErr(t, err)
	assert.True(t, first)
	todo, _, _ := s.GetTodo("todo-q")
	assert.Eq(t, TodoReady, todo.Status)
	assert.False(t, todo.Done)

	second, err := s.SetTodoReadyIfPending("todo-q")
	assert.NoErr(t, err)
	assert.False(t, second, "a ready item must not be re-queued")

	// An unknown id is a clean false, not an error.
	missing, err := s.SetTodoReadyIfPending("todo-nope")
	assert.NoErr(t, err)
	assert.False(t, missing)
}
