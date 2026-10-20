package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestUpsertJobChangeCarriesProject: a single-job write names its project on the
// Change, which is what lets a project-scoped push connection (7798 第二期) decide
// whether the write concerns it. A bulk write keeps the empty project (global).
func TestUpsertJobChangeCarriesProject(t *testing.T) {
	s := openTest(t)
	var got []Change
	s.SetChangeHook(func(c Change) { got = append(got, c) })

	assert.NoErr(t, s.UpsertJob(sampleJob("j1", "proj-a", 100)))
	assert.Len(t, got, 1)
	assert.Eq(t, ChangeKind(ChangeJob), got[0].Kind)
	assert.Eq(t, "j1", got[0].ID)
	assert.Eq(t, "proj-a", got[0].Project)

	got = nil
	_, _, _ = s.PruneJobs(RetentionPolicy{}, 0) // bulk: no single row to name
	assert.Len(t, got, 1)
	assert.Eq(t, "", got[0].Project)
}

// TestDecideHoldChangeCarriesProject: a hold decision is a single-row job write and
// names the decided job's project, re-read from the row (the decision itself carries
// no project).
func TestDecideHoldChangeCarriesProject(t *testing.T) {
	s := openTest(t)
	held := sampleJob("h1", "proj-b", 100)
	held.Status, held.UpdatedAt = statusAwaitingApproval, 100
	assert.NoErr(t, s.UpsertJob(held))

	var got []Change
	s.SetChangeHook(func(c Change) { got = append(got, c) })

	ok, err := s.DecideHold(HoldDecision{ID: "h1", To: "queued", At: 200, HoldJSON: `{}`})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Len(t, got, 1)
	assert.Eq(t, ChangeKind(ChangeJob), got[0].Kind)
	assert.Eq(t, "h1", got[0].ID)
	assert.Eq(t, "proj-b", got[0].Project)
}

// TestListJobsProjects: the multi-project restriction is one IN list (so LIMIT/OFFSET
// page over the filtered set), ANDs with the single Project filter, and leaves both
// empty filters (every project) as they were.
func TestListJobsProjects(t *testing.T) {
	s := openTest(t)
	for _, rec := range []JobRecord{
		sampleJob("a1", "proj-a", 100),
		sampleJob("b1", "proj-b", 200),
		sampleJob("c1", "proj-c", 300),
	} {
		assert.NoErr(t, s.UpsertJob(rec))
	}
	ids := func(q ListQuery) []string {
		recs, err := s.ListJobs(q)
		assert.NoErr(t, err)
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.ID)
		}
		return out
	}

	// Newest first: c1 (300), a1 (100).
	assert.Eq(t, []string{"c1", "a1"}, ids(ListQuery{Projects: []string{"proj-a", "proj-c"}}))

	// Pagination is applied by the store over the IN set, not after it.
	assert.Eq(t, []string{"a1"}, ids(ListQuery{Projects: []string{"proj-a", "proj-c"}, Limit: 1, Offset: 1}))

	// The single-project filter is unchanged.
	assert.Eq(t, []string{"b1"}, ids(ListQuery{Project: "proj-b"}))

	// Both set: ANDed, so a row must be in the list AND the single project.
	assert.Len(t, ids(ListQuery{Project: "proj-b", Projects: []string{"proj-a", "proj-c"}}), 0)

	// Neither set: every project, unchanged.
	assert.Len(t, ids(ListQuery{}), 3)
}
