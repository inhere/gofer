package overview

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// onlyProject is the caller ceiling's项目范围 predicate used by the scoped tests.
func onlyProject(project string) func(string) bool {
	return func(p string) bool { return p == project }
}

// TestBuildScopedToProjects: a caller ceiling's项目范围 keeps only its projects' jobs
// and every metric derived from them (totals, agents, the project ranking, the live
// in-progress snapshot and the review rows). The unscoped build next to it is
// unchanged, which is the regression guard for an unrestricted caller.
func TestBuildScopedToProjects(t *testing.T) {
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	now := testNow.Unix()
	put := func(id, project, status, agent string, started, ended int64, extra func(*jobstore.JobRecord)) {
		rec := jobstore.JobRecord{ID: id, ProjectKey: project, Agent: agent, Runner: "local", Status: status,
			ResultDir: "/r/" + id, StartedAt: started, EndedAt: ended}
		if extra != nil {
			extra(&rec)
		}
		assert.NoErr(t, st.UpsertJob(rec))
	}
	put("a-done", "p-a", "done", "claude", now-7200, now-3600, nil)
	put("a-live", "p-a", "running", "claude", now-60, 0, nil)
	put("b-done", "p-b", "done", "codex", now-7200, now-3600, nil)
	put("b-live", "p-b", "running", "codex", now-60, 0, nil)
	put("b-rev", "p-b", "rejected", "codex", now-5000, now-4000, func(r *jobstore.JobRecord) {
		r.ReviewedAt = now - 3000
	})

	all, err := Build(st, Query{Range: Range7d}, testNow)
	assert.NoErr(t, err)
	assert.Eq(t, 3, all.Jobs.Total)
	assert.Eq(t, 2, all.Jobs.InProgress)
	assert.Len(t, all.Projects, 2)
	assert.Eq(t, 1, all.Review.Rejected)

	scoped, err := Build(st, Query{Range: Range7d, ProjectAllowed: onlyProject("p-a")}, testNow)
	assert.NoErr(t, err)
	assert.Eq(t, 1, scoped.Jobs.Total)
	assert.Eq(t, 1, scoped.Jobs.Done)
	assert.Eq(t, 1, scoped.Jobs.InProgress)
	assert.Eq(t, 0, scoped.Review.Rejected)
	assert.Len(t, scoped.Projects, 1)
	assert.Eq(t, "p-a", scoped.Projects[0].Project)
	assert.Len(t, scoped.Agents, 1)
	assert.Eq(t, "claude", scoped.Agents[0].Agent)
}

// TestCacheKeySeparatesScopes: two requests that differ only in the caller's project
// scope must not share a cache entry, while an unscoped request keeps the legacy
// (range, tz) key.
func TestCacheKeySeparatesScopes(t *testing.T) {
	var builds int
	s, _, _ := newCachedService(func(q Query, at time.Time) (*Overview, error) {
		builds++
		return &Overview{Range: RangeInfo{Key: q.Range}, Notes: []string{q.ScopeKey}}, nil
	})

	first, err := s.Get(Query{Range: Range7d, ProjectAllowed: onlyProject("p-a"), ScopeKey: "p-a"})
	assert.NoErr(t, err)
	assert.False(t, first.Cached)

	second, err := s.Get(Query{Range: Range7d, ProjectAllowed: onlyProject("p-b"), ScopeKey: "p-b"})
	assert.NoErr(t, err)
	assert.False(t, second.Cached, "a different scope must be a cache miss")
	assert.Eq(t, []string{"p-b"}, second.Notes)

	// The same scope is served from the cache.
	again, err := s.Get(Query{Range: Range7d, ProjectAllowed: onlyProject("p-a"), ScopeKey: "p-a"})
	assert.NoErr(t, err)
	assert.True(t, again.Cached)

	// An unscoped query keeps its own entry (empty scope key).
	plain, err := s.Get(Query{Range: Range7d})
	assert.NoErr(t, err)
	assert.False(t, plain.Cached)
	plainAgain, err := s.Get(Query{Range: Range7d})
	assert.NoErr(t, err)
	assert.True(t, plainAgain.Cached)
	assert.Eq(t, 3, builds)
}
