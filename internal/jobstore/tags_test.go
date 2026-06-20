package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestUpsertGetTagsJSON covers the TagsJSON round-trip (E5): a record carrying a
// JSON tags array reads back identical; an empty one stays empty.
func TestUpsertGetTagsJSON(t *testing.T) {
	s := openTest(t)

	in := sampleJob("j-tags", "proj", 700)
	in.TagsJSON = `["a","b"]`
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("j-tags")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, `["a","b"]`, got.TagsJSON)

	// Empty tags_json round-trips as "".
	empty := sampleJob("j-notags", "proj", 600)
	assert.NoErr(t, s.UpsertJob(empty))
	gotEmpty, ok, err := s.GetJob("j-notags")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", gotEmpty.TagsJSON)
}

// TestListQueryTagFilter asserts ListQuery.Tag matches a tag element via the
// tags_json LIKE '%"<tag>"%' predicate, and that the quoting prevents a
// substring false-positive (querying `a` must NOT match `["ab"]`).
func TestListQueryTagFilter(t *testing.T) {
	s := openTest(t)

	ja := sampleJob("ja", "p", 100)
	ja.TagsJSON = `["a","b"]`
	jb := sampleJob("jb", "p", 200)
	jb.TagsJSON = `["ab"]` // substring "a" — must NOT match a query for tag=a.
	jc := sampleJob("jc", "p", 300)
	jc.TagsJSON = `["c"]`
	assert.NoErr(t, s.UpsertJob(ja))
	assert.NoErr(t, s.UpsertJob(jb))
	assert.NoErr(t, s.UpsertJob(jc))

	// tag=a -> only ja (ja has "a"; jb has only "ab", quoting blocks the substring).
	a, err := s.ListJobs(ListQuery{Tag: "a"})
	assert.NoErr(t, err)
	assert.Len(t, a, 1)
	assert.Eq(t, "ja", a[0].ID)

	// tag=b -> only ja.
	b, err := s.ListJobs(ListQuery{Tag: "b"})
	assert.NoErr(t, err)
	assert.Len(t, b, 1)
	assert.Eq(t, "ja", b[0].ID)

	// tag=ab -> only jb (the quoted element match works for the longer token too).
	ab, err := s.ListJobs(ListQuery{Tag: "ab"})
	assert.NoErr(t, err)
	assert.Len(t, ab, 1)
	assert.Eq(t, "jb", ab[0].ID)

	// unknown tag -> none.
	none, err := s.ListJobs(ListQuery{Tag: "zzz"})
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
}

// TestListQueryAgentRunnerSinceFilter asserts the E5 agent/runner/since filters
// each constrain ListJobs exactly.
func TestListQueryAgentRunnerSinceFilter(t *testing.T) {
	s := openTest(t)

	j1 := sampleJob("j1", "p", 100) // agent=claude runner=local
	j2 := sampleJob("j2", "p", 200)
	j2.Agent = "exec"
	j2.Runner = "worker"
	j3 := sampleJob("j3", "p", 300) // agent=claude runner=local
	assert.NoErr(t, s.UpsertJob(j1))
	assert.NoErr(t, s.UpsertJob(j2))
	assert.NoErr(t, s.UpsertJob(j3))

	// agent=exec -> only j2.
	exec, err := s.ListJobs(ListQuery{Agent: "exec"})
	assert.NoErr(t, err)
	assert.Len(t, exec, 1)
	assert.Eq(t, "j2", exec[0].ID)

	// runner=worker -> only j2.
	worker, err := s.ListJobs(ListQuery{Runner: "worker"})
	assert.NoErr(t, err)
	assert.Len(t, worker, 1)
	assert.Eq(t, "j2", worker[0].ID)

	// agent=claude -> j1 + j3.
	claude, err := s.ListJobs(ListQuery{Agent: "claude"})
	assert.NoErr(t, err)
	assert.Len(t, claude, 2)

	// since=200 -> j2 + j3 (started_at >= 200), newest first.
	since, err := s.ListJobs(ListQuery{Since: 200})
	assert.NoErr(t, err)
	assert.Len(t, since, 2)
	assert.Eq(t, "j3", since[0].ID)
	assert.Eq(t, "j2", since[1].ID)

	// Combined agent + runner with no overlap -> none.
	none, err := s.ListJobs(ListQuery{Agent: "claude", Runner: "worker"})
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
}
