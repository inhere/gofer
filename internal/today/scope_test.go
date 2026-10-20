package today

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// onlyProject is the caller ceiling's项目范围 predicate used by the scoped tests.
func onlyProject(project string) func(string) bool {
	return func(p string) bool { return p == project }
}

// TestFilterScope: a nil predicate keeps the queue and the snoozed list byte-identical
// (an unrestricted caller), while a predicate keeps only its projects and drops the
// project-less global summary cards (their counts cover every project).
func TestFilterScope(t *testing.T) {
	cards := []Card{
		{Key: "review:j1", ProjectKey: "p-a"},
		{Key: "review:j2", ProjectKey: "p-b"},
		{Key: CaptureCardKey},
	}
	snoozed := []SnoozedCard{
		{CardKey: "review:j1", ProjectKey: "p-a"},
		{CardKey: "review:j2", ProjectKey: "p-b"},
	}

	keptCards, keptSnoozed := filterScope(cards, snoozed, nil)
	assert.Eq(t, []string{"review:j1", "review:j2", CaptureCardKey}, keys(keptCards))
	assert.Len(t, keptSnoozed, 2)

	keptCards, keptSnoozed = filterScope(cards, snoozed, onlyProject("p-a"))
	assert.Eq(t, []string{"review:j1"}, keys(keptCards))
	assert.Len(t, keptSnoozed, 1)
	assert.Eq(t, "review:j1", keptSnoozed[0].CardKey)
}

// TestTodayScopedCards: GET /v1/today for a restricted caller lists only its own
// project's cards; the unrestricted call lists both.
func TestTodayScopedCards(t *testing.T) {
	svc, st := newTestService(t)
	putJob(t, st, jobstore.JobRecord{ID: "j-a", ProjectKey: "p-a", Agent: "claude", Runner: "local",
		Status: "needs_review", ResultDir: "/r/j-a", EndedAt: ago(10)})
	putJob(t, st, jobstore.JobRecord{ID: "j-b", ProjectKey: "p-b", Agent: "claude", Runner: "local",
		Status: "needs_review", ResultDir: "/r/j-b", EndedAt: ago(10)})

	all, err := svc.Today(Query{})
	assert.NoErr(t, err)
	assert.True(t, hasKey(all.Decisions, KindReview+":j-a"))
	assert.True(t, hasKey(all.Decisions, KindReview+":j-b"))

	scoped, err := svc.Today(Query{ProjectAllowed: onlyProject("p-a")})
	assert.NoErr(t, err)
	assert.True(t, hasKey(scoped.Decisions, KindReview+":j-a"))
	assert.False(t, hasKey(scoped.Decisions, KindReview+":j-b"))
}

func hasKey(cards []Card, key string) bool {
	_, ok := find(cards, key)
	return ok
}

// TestHandledScoped: the 「已处理」 rows carry the card's project and a restricted
// caller sees only its own; the unscoped read is unchanged.
func TestHandledScoped(t *testing.T) {
	svc, _ := newTestService(t)
	for _, in := range []ActionInput{
		{CardKey: KindReview + ":j-a", ActionID: "accept", Project: "p-a"},
		{CardKey: KindReview + ":j-b", ActionID: "accept", Project: "p-b"},
		{CardKey: CaptureCardKey, ActionID: "open"},
	} {
		_, err := svc.RecordAction(in, "u1")
		assert.NoErr(t, err)
	}

	all, err := svc.HandledSince(7)
	assert.NoErr(t, err)
	assert.Len(t, all, 3)

	scoped, err := svc.HandledSinceScoped(7, onlyProject("p-a"))
	assert.NoErr(t, err)
	assert.Len(t, scoped, 1)
	assert.Eq(t, "p-a", scoped[0].Project)
}

// TestLanesScoped: the lanes list and its header summary are both restricted — the
// summary is recomputed over the kept lanes, never over every lane the server knows.
func TestLanesScoped(t *testing.T) {
	f := newFixture(t)
	for _, p := range []struct{ id, project string }{{"pl-a", "p-a"}, {"pl-b", "p-b"}} {
		assert.NoErr(t, f.st.InsertPlan(jobstore.Plan{PlanID: p.id, Title: p.id, Status: jobstore.PlanBlocked,
			ProjectKey: p.project, CreatedAt: f.now.Unix(), UpdatedAt: f.now.Unix()}))
	}

	all, err := f.b.Build()
	assert.NoErr(t, err)
	assert.Len(t, all.Lanes, 2)
	assert.Eq(t, 2, all.Summary.Total)

	scoped, err := f.b.BuildFor(onlyProject("p-a"))
	assert.NoErr(t, err)
	assert.Len(t, scoped.Lanes, 1)
	assert.Eq(t, "pl-a", scoped.Lanes[0].ID)
	assert.Eq(t, 1, scoped.Summary.Total)
}

// TestHandledProjectPersisted guards the stored shape: the project travels in the
// audit detail, so the scope filter needs no card lookup.
func TestHandledProjectPersisted(t *testing.T) {
	svc, st := newTestService(t)
	_, err := svc.RecordAction(ActionInput{CardKey: KindReview + ":j1", ActionID: "accept", Project: "p-x"}, "u1")
	assert.NoErr(t, err)
	rows, err := st.ListAuditEventsSince(jobstore.TodayActionAudit, 0, 10)
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
	assert.Contains(t, rows[0].Detail, `"project":"p-x"`)
}
