package jobstore

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// strPtr returns a pointer to s: a TodoPatch field is a pointer, where nil means "not
// sent" and a non-nil empty value means "clear".
func strPtr(s string) *string { return &s }

// todoOrFail reads a todo and fails the test when it is missing.
func todoOrFail(t *testing.T, s *Store, todoID string) PlanTodo {
	t.Helper()
	td, ok, err := s.GetTodo(todoID)
	assert.NoErr(t, err)
	assert.True(t, ok, "todo %s exists", todoID)
	return td
}

// seedContinuePlan creates a plan plus plain todos (id = title), which is all the
// continuation cases need: they reference ids, they do not dispatch anything.
func seedContinuePlan(t *testing.T, s *Store, planID string, todoIDs ...string) {
	t.Helper()
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: planID, Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	for i, id := range todoIDs {
		assert.NoErr(t, s.InsertTodo(PlanTodo{
			TodoID: id, PlanID: planID, Title: id, Sort: i + 1,
			CreatedAt: int64(i + 1), UpdatedAt: int64(i + 1),
		}))
	}
}

// setContinueFrom applies a continue_from patch and requires it to be accepted.
func setContinueFrom(t *testing.T, s *Store, todoID, prevID string) {
	t.Helper()
	ok, err := s.UpdateTodoPatch(todoID, TodoPatch{ContinueFrom: strPtr(prevID)})
	assert.NoErr(t, err)
	assert.True(t, ok, "patch continue_from on %s", todoID)
}

// claimContinue reports whether the claim won; a claim never returns an error here
// (racing claims report ok=false, they do not fail).
func claimContinue(t *testing.T, s *Store, predID, succID string) bool {
	t.Helper()
	ok, err := s.ClaimTodoContinuation(predID, succID)
	assert.NoErr(t, err)
	return ok
}

// TestTodoContinueFieldsRoundTripAndPatch: the three continuation columns survive a
// write/read, continue_from is settable and clearable through the shared TodoPatch (the
// HTTP / CLI / MCP write path), and the two derived columns stay with their dedicated
// writers — a todo carrying them into InsertTodo does not smuggle them in.
func TestTodoContinueFieldsRoundTripAndPatch(t *testing.T) {
	s := openTest(t)
	seedContinuePlan(t, s, "plan-c", "todo-prev", "todo-next", "todo-plain")

	// A plain item reads all three back empty (the pre-column shape).
	plain := todoOrFail(t, s, "todo-plain")
	assert.Eq(t, "", plain.ContinueFrom)
	assert.Eq(t, "", plain.ContinuedBy)
	assert.Eq(t, "", plain.ContinueNote)

	// Set through a patch — and the implied `after` dependency comes with it.
	setContinueFrom(t, s, "todo-next", "todo-prev")
	got := todoOrFail(t, s, "todo-next")
	assert.Eq(t, "todo-prev", got.ContinueFrom)
	assert.Eq(t, []string{"todo-prev"}, got.After)
	assert.Eq(t, "", got.ContinuedBy)
	assert.Eq(t, "", got.ContinueNote)
	assert.False(t, TodoPatch{ContinueFrom: strPtr("todo-prev")}.Empty(),
		"continue_from alone is a real update, not a no-op")

	// Create-time: continue_from is stored (validated, with its dependency), while
	// continued_by / continue_note are NOT — ClaimTodoContinuation and
	// SetTodoContinueNote own those two.
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "todo-ins", PlanID: "plan-c", Title: "ins", Sort: 4,
		ContinueFrom: "todo-prev", ContinuedBy: "todo-x", ContinueNote: "not mine",
		CreatedAt: 4, UpdatedAt: 4,
	}))
	ins := todoOrFail(t, s, "todo-ins")
	assert.Eq(t, "todo-prev", ins.ContinueFrom)
	assert.Eq(t, []string{"todo-prev"}, ins.After)
	assert.Eq(t, "", ins.ContinuedBy)
	assert.Eq(t, "", ins.ContinueNote)

	// Clear with a non-nil empty string; `after` is NOT touched (the dependency stays
	// declared, see TestTodoContinueAutoAfter).
	ok, err := s.UpdateTodoPatch("todo-next", TodoPatch{ContinueFrom: strPtr("")})
	assert.NoErr(t, err)
	assert.True(t, ok)
	cleared := todoOrFail(t, s, "todo-next")
	assert.Eq(t, "", cleared.ContinueFrom)
	assert.Eq(t, []string{"todo-prev"}, cleared.After)

	// An unknown todo is a clean ok=false, no error, nothing written.
	missingOK, err := s.UpdateTodoPatch("todo-nope", TodoPatch{ContinueFrom: strPtr("todo-prev")})
	assert.NoErr(t, err)
	assert.False(t, missingOK)

	// ApplyTodoPatch is the create-path twin: it copies the reference, and InsertTodo
	// applies the rules to it.
	var fresh PlanTodo
	fresh.ApplyTodoPatch(TodoPatch{ContinueFrom: strPtr("todo-prev")})
	assert.Eq(t, "todo-prev", fresh.ContinueFrom)
}

// TestTodoContinueFromValidation covers the three write-time rules — the target is a todo
// of the same plan, never the item itself, never a cycle — on BOTH write paths (insert and
// patch), each reported through its own exported sentinel.
func TestTodoContinueFromValidation(t *testing.T) {
	s := openTest(t)
	seedContinuePlan(t, s, "plan-v", "v1", "v2", "v3")
	seedContinuePlan(t, s, "plan-v2", "other")

	// 1) the target must be a todo of the SAME plan: missing and cross-plan are the same
	// rule broken, and a rejected reference is not stored.
	ok, err := s.UpdateTodoPatch("v2", TodoPatch{ContinueFrom: strPtr("v-nope")})
	assert.False(t, ok)
	assert.ErrIs(t, err, ErrTodoContinueNotInPlan)
	_, err = s.UpdateTodoPatch("v2", TodoPatch{ContinueFrom: strPtr("other")})
	assert.ErrIs(t, err, ErrTodoContinueNotInPlan)
	assert.Eq(t, "", todoOrFail(t, s, "v2").ContinueFrom, "a rejected reference must not be stored")
	assert.Eq(t, 0, len(todoOrFail(t, s, "v2").After), "a rejected reference adds no dependency")

	// The insert path applies the same rule and creates no row when it fails.
	assert.ErrIs(t, s.InsertTodo(PlanTodo{
		TodoID: "v-ins", PlanID: "plan-v", Title: "ins", ContinueFrom: "other",
		CreatedAt: 9, UpdatedAt: 9,
	}), ErrTodoContinueNotInPlan)
	_, okIns, err := s.GetTodo("v-ins")
	assert.NoErr(t, err)
	assert.False(t, okIns, "a rejected insert must not create the todo")

	// 2) an item cannot continue itself.
	_, err = s.UpdateTodoPatch("v2", TodoPatch{ContinueFrom: strPtr("v2")})
	assert.ErrIs(t, err, ErrTodoContinueSelf)
	assert.ErrIs(t, s.InsertTodo(PlanTodo{
		TodoID: "v-self", PlanID: "plan-v", Title: "self", ContinueFrom: "v-self",
		CreatedAt: 9, UpdatedAt: 9,
	}), ErrTodoContinueSelf)

	// 3) no cycle: v3 → v2 → v1 is a legitimate chain, closing it with v1 → v3 is not.
	setContinueFrom(t, s, "v2", "v1")
	setContinueFrom(t, s, "v3", "v2")
	_, err = s.UpdateTodoPatch("v1", TodoPatch{ContinueFrom: strPtr("v3")})
	assert.ErrIs(t, err, ErrTodoContinueCycle)
	assert.Eq(t, "", todoOrFail(t, s, "v1").ContinueFrom)

	// The insert path runs the same walk: a new item may join the chain anywhere (it can
	// never close a cycle, since nothing continues it yet), and it is still validated.
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "v5", PlanID: "plan-v", Title: "5", ContinueFrom: "v3",
		CreatedAt: 9, UpdatedAt: 9,
	}))
	assert.Eq(t, "v3", todoOrFail(t, s, "v5").ContinueFrom)

	// Re-setting the same reference is not a self-reference and not a cycle.
	setContinueFrom(t, s, "v3", "v2")

	// A cycle that does NOT involve the item is somebody else's problem: closing it is not
	// this write's business, and following it further would only loop.
	setContinueFrom(t, s, "v5", "v3")

	// The error text names the rule, and the sentinel survives the wrapping.
	_, err = s.UpdateTodoPatch("v1", TodoPatch{ContinueFrom: strPtr("v-nope")})
	assert.ErrMsgContains(t, err, "continue_from")
}

// TestTodoContinueAutoAfter: continuing an item means waiting for it, so setting
// continue_from adds the predecessor to `after` (never twice, and merging with an
// explicit After in the same patch); clearing it leaves `after` alone.
func TestTodoContinueAutoAfter(t *testing.T) {
	s := openTest(t)
	seedContinuePlan(t, s, "plan-a", "a1", "a2", "a3", "a4", "a5")

	// Setting adds the predecessor.
	setContinueFrom(t, s, "a2", "a1")
	assert.Eq(t, []string{"a1"}, todoOrFail(t, s, "a2").After)

	// Already in `after`: no duplicate.
	already := []string{"a1"}
	ok, err := s.UpdateTodoPatch("a3", TodoPatch{ContinueFrom: strPtr("a1"), After: &already})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, []string{"a1"}, todoOrFail(t, s, "a3").After)

	// An explicit After and the implied dependency merge (the patch may carry both).
	own := []string{"a2", "a9"}
	ok, err = s.UpdateTodoPatch("a4", TodoPatch{ContinueFrom: strPtr("a1"), After: &own})
	assert.NoErr(t, err)
	assert.True(t, ok)
	merged := todoOrFail(t, s, "a4").After
	assert.Len(t, merged, 3)
	assert.ContainsElems(t, merged, []string{"a2", "a9", "a1"})

	// The insert path adds it too, and never appends to the caller's own slice.
	callerOwn := []string{"a5"}
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "a6", PlanID: "plan-a", Title: "6", After: callerOwn, ContinueFrom: "a2",
		CreatedAt: 6, UpdatedAt: 6,
	}))
	assert.Eq(t, []string{"a5"}, callerOwn, "the caller's slice is left alone")
	assert.ContainsElems(t, todoOrFail(t, s, "a6").After, []string{"a5", "a2"})

	// Clearing drops the reference and keeps the dependency it had implied.
	ok, err = s.UpdateTodoPatch("a2", TodoPatch{ContinueFrom: strPtr("")})
	assert.NoErr(t, err)
	assert.True(t, ok)
	a2 := todoOrFail(t, s, "a2")
	assert.Eq(t, "", a2.ContinueFrom)
	assert.Eq(t, []string{"a1"}, a2.After)
}

// TestClaimTodoContinuationConcurrent: one predecessor session is continued at most once.
// A crowd of callers racing for the same slot leaves exactly one winner — the conditional
// UPDATE decides, not the client — and every loser is told ok=false, not an error.
func TestClaimTodoContinuationConcurrent(t *testing.T) {
	s := openTest(t)
	seedContinuePlan(t, s, "plan-claim", "c-prev", "c-prev2", "c-a", "c-b", "c-c", "c-d", "c-solo")

	const rounds = 2
	succs := []string{"c-a", "c-b", "c-c", "c-d"}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
		errs    []error
	)
	for round := 0; round < rounds; round++ {
		for _, succID := range succs {
			wg.Add(1)
			go func(succID string) {
				defer wg.Done()
				ok, err := s.ClaimTodoContinuation("c-prev", succID)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs = append(errs, err)
					return
				}
				if ok {
					winners = append(winners, succID)
				}
			}(succID)
		}
	}
	wg.Wait()
	assert.Len(t, errs, 0, "a claim never fails, it loses: %v", errs)
	assert.Len(t, winners, 1, "%d racing claims won", rounds*len(succs))
	assert.Eq(t, winners[0], todoOrFail(t, s, "c-prev").ContinuedBy)

	// A second predecessor is independent: its slot is free and only its first claim wins.
	assert.True(t, claimContinue(t, s, "c-prev2", "c-solo"))
	assert.False(t, claimContinue(t, s, "c-prev2", "c-solo"), "the holder must not re-claim its own slot")
	assert.False(t, claimContinue(t, s, "c-prev2", "c-a"), "a slot taken by another item stays taken")
	assert.Eq(t, "c-solo", todoOrFail(t, s, "c-prev2").ContinuedBy)

	// An unknown predecessor (or a deleted one) is a clean loss; an empty id is a bug.
	lost, err := s.ClaimTodoContinuation("c-nope", "c-solo")
	assert.NoErr(t, err)
	assert.False(t, lost)
	_, err = s.ClaimTodoContinuation("", "c-solo")
	assert.Err(t, err)
	_, err = s.ClaimTodoContinuation("c-prev2", "")
	assert.Err(t, err)
}

// TestReleaseTodoContinuationOnlyOwner: releasing gives a slot back ONLY to the item that
// holds it, and a release that matches nothing is a no-op rather than an error (a caller
// stepping back must not have to ask who owns the slot first).
func TestReleaseTodoContinuationOnlyOwner(t *testing.T) {
	s := openTest(t)
	seedContinuePlan(t, s, "plan-rel", "be-prev", "be-a", "be-b")

	assert.True(t, claimContinue(t, s, "be-prev", "be-a"))

	// Another item cannot free someone else's slot.
	assert.NoErr(t, s.ReleaseTodoContinuation("be-prev", "be-b"))
	assert.Eq(t, "be-a", todoOrFail(t, s, "be-prev").ContinuedBy, "the holder keeps the slot")

	// The holder frees it, and the release is idempotent.
	assert.NoErr(t, s.ReleaseTodoContinuation("be-prev", "be-a"))
	assert.Eq(t, "", todoOrFail(t, s, "be-prev").ContinuedBy)
	assert.NoErr(t, s.ReleaseTodoContinuation("be-prev", "be-a"))
	assert.NoErr(t, s.ReleaseTodoContinuation("be-nope", "be-a"), "a gone predecessor is nothing to free")

	// Once freed the slot is takeable again.
	assert.True(t, claimContinue(t, s, "be-prev", "be-b"))
	assert.Eq(t, "be-b", todoOrFail(t, s, "be-prev").ContinuedBy)

	// Empty ids are a bug, not a silent no-op.
	assert.Err(t, s.ReleaseTodoContinuation("", "be-b"))
	assert.Err(t, s.ReleaseTodoContinuation("be-prev", ""))
}

// TestTodoContinueSlotReleasedOnChangeAndDelete: the slot travels with the reference.
// Switching or clearing continue_from (or deleting the item that held the slot) gives the
// OLD predecessor its slot back; deleting a predecessor leaves its successor's
// continue_from dangling on purpose, ready for the dispatch-time "nothing to continue
// from" fallback.
func TestTodoContinueSlotReleasedOnChangeAndDelete(t *testing.T) {
	s := openTest(t)
	seedContinuePlan(t, s, "plan-slot", "r1", "r2", "r3", "r4", "r5", "r6")

	// Switching the reference frees the slot held on the former predecessor.
	setContinueFrom(t, s, "r2", "r1")
	assert.True(t, claimContinue(t, s, "r1", "r2"))
	setContinueFrom(t, s, "r2", "r3")
	assert.Eq(t, "", todoOrFail(t, s, "r1").ContinuedBy, "the former predecessor must be continuable again")
	assert.ContainsElems(t, todoOrFail(t, s, "r2").After, []string{"r1", "r3"})

	// Another item takes the freed slot; r4 is the item that r1's later deletion leaves
	// dangling.
	setContinueFrom(t, s, "r4", "r1")
	assert.True(t, claimContinue(t, s, "r1", "r4"), "someone else can take the freed slot")

	// Clearing the reference frees its slot as well, and keeps `after`.
	setContinueFrom(t, s, "r6", "r5")
	assert.True(t, claimContinue(t, s, "r5", "r6"))
	ok, err := s.UpdateTodoPatch("r6", TodoPatch{ContinueFrom: strPtr("")})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", todoOrFail(t, s, "r5").ContinuedBy)
	assert.Eq(t, []string{"r5"}, todoOrFail(t, s, "r6").After)

	// Deleting the item that HELD a slot frees that slot.
	assert.True(t, claimContinue(t, s, "r3", "r2"))
	deleted, err := s.DeleteTodo("r2")
	assert.NoErr(t, err)
	assert.True(t, deleted)
	assert.Eq(t, "", todoOrFail(t, s, "r3").ContinuedBy, "deleting the successor frees the predecessor")

	// Deleting a PREDECESSOR keeps the successor's reference (dangling), so a re-created
	// item under the same id would still be continued; dispatch reads the missing one as
	// "no session to continue from". r4 continues r1 and holds its slot (taken above).
	assert.Eq(t, "r4", todoOrFail(t, s, "r1").ContinuedBy)
	deleted, err = s.DeleteTodo("r1")
	assert.NoErr(t, err)
	assert.True(t, deleted)
	_, ok, err = s.GetTodo("r1")
	assert.NoErr(t, err)
	assert.False(t, ok)
	assert.Eq(t, "r1", todoOrFail(t, s, "r4").ContinueFrom, "the dangling reference is kept")
	assert.ContainsElems(t, todoOrFail(t, s, "r4").After, []string{"r1"})

	// A dangling STORED reference is tolerated (and moving away from it releases the slot
	// it held, if the predecessor is still there), but naming a deleted todo as a NEW
	// predecessor is not: the item no longer exists in the plan.
	setContinueFrom(t, s, "r4", "r3")
	assert.Eq(t, "r3", todoOrFail(t, s, "r4").ContinueFrom)
	_, err = s.UpdateTodoPatch("r6", TodoPatch{ContinueFrom: strPtr("r1")})
	assert.ErrIs(t, err, ErrTodoContinueNotInPlan)
}

// TestSetTodoContinueNote: the reason a dispatch did not continue is written and cleared
// only through its own setter, stays out of Note / DispatchError, and an unknown item is
// surfaced instead of silently dropped.
func TestSetTodoContinueNote(t *testing.T) {
	s := openTest(t)
	seedContinuePlan(t, s, "plan-note", "n1", "n2")

	assert.NoErr(t, s.SetTodoContinueNote("n2", "未接续：前序未完成，已开新会话"))
	assert.Eq(t, "未接续：前序未完成，已开新会话", todoOrFail(t, s, "n2").ContinueNote)

	// Overwritten (it is "the last dispatch's reason", not a log) and clearable.
	assert.NoErr(t, s.SetTodoContinueNote("n2", "未接续：链太长"))
	assert.Eq(t, "未接续：链太长", todoOrFail(t, s, "n2").ContinueNote)
	assert.NoErr(t, s.SetTodoContinueNote("n2", ""))
	assert.Eq(t, "", todoOrFail(t, s, "n2").ContinueNote)

	// Setting the same value twice still finds the todo (the row count is not zero just
	// because the value did not change).
	assert.NoErr(t, s.SetTodoContinueNote("n2", "same"))
	assert.NoErr(t, s.SetTodoContinueNote("n2", "same"))
	assert.Eq(t, "same", todoOrFail(t, s, "n2").ContinueNote)

	// It never touches the item's outcome note or its dispatch error.
	setErrOK, err := s.SetTodoDispatchError("n2", "boom")
	assert.NoErr(t, err)
	assert.True(t, setErrOK)
	assert.NoErr(t, s.SetTodoContinueNote("n2", "未接续：x"))
	touched := todoOrFail(t, s, "n2")
	assert.Eq(t, "boom", touched.DispatchError)
	assert.Eq(t, "", touched.Note)
	assert.Eq(t, "", touched.ContinuedBy)

	// An unknown item is an error, not a silent no-op.
	assert.Err(t, s.SetTodoContinueNote("n-nope", "x"))
	assert.Err(t, s.SetTodoContinueNote("", "x"))
}

// TestMigrateAddsContinueColumnsToOldPlanTodos simulates a plan_todos table that predates
// the continuation columns: Open adds them additively, the old row reads back as an
// ordinary uncontinued item (NULL columns, i.e. the COALESCE'd claim must treat it as
// free), and the migrated table is fully usable.
func TestMigrateAddsContinueColumnsToOldPlanTodos(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-continue.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE plan_todos (
	  todo_id    TEXT PRIMARY KEY,
	  plan_id    TEXT NOT NULL,
	  job_id     TEXT,
	  title      TEXT,
	  done       INTEGER NOT NULL DEFAULT 0,
	  sort       INTEGER NOT NULL DEFAULT 0,
	  created_at INTEGER NOT NULL,
	  updated_at INTEGER NOT NULL
	)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO plan_todos (todo_id, plan_id, title, done, sort, created_at, updated_at)
	  VALUES ('t-old', 'plan-old', 'old item', 1, 1, 1000, 1000)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()
	assert.True(t, tableHasColumn(t, s, "plan_todos", "continue_from"))
	assert.True(t, tableHasColumn(t, s, "plan_todos", "continued_by"))
	assert.True(t, tableHasColumn(t, s, "plan_todos", "continue_note"))

	old, ok, err := s.GetTodo("t-old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", old.ContinueFrom)
	assert.Eq(t, "", old.ContinuedBy)
	assert.Eq(t, "", old.ContinueNote)

	// The migrated table takes a continuation: reference stored, dependency implied, and
	// the legacy row's NULL slot is claimed by inference (COALESCE), not rejected.
	assert.NoErr(t, s.InsertTodo(PlanTodo{
		TodoID: "t-new", PlanID: "plan-old", Title: "new item", ContinueFrom: "t-old",
		CreatedAt: 1001, UpdatedAt: 1001,
	}))
	fresh := todoOrFail(t, s, "t-new")
	assert.Eq(t, "t-old", fresh.ContinueFrom)
	assert.Eq(t, []string{"t-old"}, fresh.After)
	assert.True(t, claimContinue(t, s, "t-old", "t-new"))
	assert.NoErr(t, s.SetTodoContinueNote("t-new", "未接续：链太长"))
	assert.Eq(t, "未接续：链太长", todoOrFail(t, s, "t-new").ContinueNote)

	// Idempotent: a second migration pass changes nothing.
	assert.NoErr(t, s.migrate())
	assert.Eq(t, "t-new", todoOrFail(t, s, "t-old").ContinuedBy)
}
