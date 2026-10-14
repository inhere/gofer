package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestTodoPrimeKeywordsRoundTripAndPatch (gofer-11tz): the task keywords of an item's
// job store on insert, patch and clear with a non-nil empty list — they select the
// tracker-prime the dispatched job receives.
func TestTodoPrimeKeywordsRoundTripAndPatch(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-pk", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{TodoID: "t-pk", PlanID: "plan-pk", Title: "p",
		PrimeKeywords: []string{"internal/job", "rules"}, Sort: 1, CreatedAt: 1, UpdatedAt: 1}))

	got, _, err := s.GetTodo("t-pk")
	assert.NoErr(t, err)
	assert.Eq(t, []string{"internal/job", "rules"}, got.PrimeKeywords)

	next := []string{"web"}
	assert.False(t, TodoPatch{PrimeKeywords: &next}.Empty())
	ok, err := s.UpdateTodoPatch("t-pk", TodoPatch{PrimeKeywords: &next})
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _, _ = s.GetTodo("t-pk")
	assert.Eq(t, next, got.PrimeKeywords)

	// A non-nil empty list clears them (the todo no longer names anything).
	none := []string{}
	_, err = s.UpdateTodoPatch("t-pk", TodoPatch{PrimeKeywords: &none})
	assert.NoErr(t, err)
	got, _, _ = s.GetTodo("t-pk")
	assert.Nil(t, got.PrimeKeywords)

	var fresh PlanTodo
	fresh.ApplyTodoPatch(TodoPatch{PrimeKeywords: &next})
	assert.Eq(t, next, fresh.PrimeKeywords)
}
