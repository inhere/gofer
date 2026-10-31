package jobstore

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func beginIn(id, digest string, now int64) MemorySynthesisBegin {
	return MemorySynthesisBegin{ID: id, ProjectKey: "proj", Day: "2026-10-28", InputDigest: digest, MaterialSchema: "daily-memory-materials.v1", Now: now}
}

func TestBeginMemorySynthesisLifecycle(t *testing.T) {
	s := openTest(t)
	r, out, err := s.BeginMemorySynthesisIfChanged(beginIn("r1", "d1", 100))
	assert.NoErr(t, err)
	assert.Eq(t, MemorySynthesisStarted, out)
	assert.Eq(t, "running", r.Status)
	assert.Eq(t, 1, r.Attempt)

	// A second begin for the same day sees the live run, whatever its digest.
	r2, out, err := s.BeginMemorySynthesisIfChanged(beginIn("r2", "d2", 101))
	assert.NoErr(t, err)
	assert.Eq(t, MemorySynthesisAlreadyRunning, out)
	assert.Eq(t, "r1", r2.ID)

	done, err := s.FinishMemorySynthesisRun("r1", MemorySynthesisFinish{Status: "succeeded", Agent: "fake", Model: "m", ResultJSON: `{"a":1}`, OutputDigest: "od", Now: 110})
	assert.NoErr(t, err)
	assert.Eq(t, "succeeded", done.Status)
	assert.Eq(t, int64(110), done.FinishedAt)
	assert.Eq(t, "fake", done.Agent)

	// Finish is a compare-and-set: a finished record cannot be finished again.
	_, err = s.FinishMemorySynthesisRun("r1", MemorySynthesisFinish{Status: "failed", Now: 120})
	assert.Eq(t, ErrMemorySynthesisNotRunning, err)
	_, err = s.FinishMemorySynthesisRun("nope", MemorySynthesisFinish{Status: "failed"})
	assert.Eq(t, ErrMemorySynthesisNotRunning, err)
	_, err = s.FinishMemorySynthesisRun("r1", MemorySynthesisFinish{Status: "running"})
	assert.Err(t, err)

	// Same digest: the succeeded record is reused and only last_checked_at moves.
	r3, out, err := s.BeginMemorySynthesisIfChanged(beginIn("r3", "d1", 200))
	assert.NoErr(t, err)
	assert.Eq(t, MemorySynthesisReused, out)
	assert.Eq(t, "r1", r3.ID)
	got, _, _ := s.GetMemorySynthesisRun("r1")
	assert.Eq(t, int64(200), got.LastCheckedAt)
	assert.Eq(t, int64(110), got.FinishedAt)

	// A different digest starts attempt 2.
	r4, out, err := s.BeginMemorySynthesisIfChanged(beginIn("r4", "d3", 300))
	assert.NoErr(t, err)
	assert.Eq(t, MemorySynthesisStarted, out)
	assert.Eq(t, 2, r4.Attempt)

	// Snapshot is stored only when the caller passes one.
	in := beginIn("r5", "d9", 301)
	in.ProjectKey = "other"
	in.MaterialSnapshot = `{"x":1}`
	r5, _, err := s.BeginMemorySynthesisIfChanged(in)
	assert.NoErr(t, err)
	assert.Eq(t, `{"x":1}`, r5.MaterialSnapshot)
	got4, _, _ := s.GetMemorySynthesisRun("r4")
	assert.Eq(t, "", got4.MaterialSnapshot)

	_, _, err = s.BeginMemorySynthesisIfChanged(MemorySynthesisBegin{ProjectKey: "p"})
	assert.Err(t, err)
}

func TestBeginMemorySynthesisFailsStaleRunning(t *testing.T) {
	s := openTest(t)
	_, _, err := s.BeginMemorySynthesisIfChanged(beginIn("old", "d1", 100))
	assert.NoErr(t, err)
	in := beginIn("new", "d1", 5000)
	in.StaleBefore = 4000
	r, out, err := s.BeginMemorySynthesisIfChanged(in)
	assert.NoErr(t, err)
	assert.Eq(t, MemorySynthesisStarted, out)
	assert.Eq(t, "new", r.ID)
	old, _, _ := s.GetMemorySynthesisRun("old")
	assert.Eq(t, "failed", old.Status)
	assert.Eq(t, "timed out", old.Error)
}

func TestMemorySynthesisUniqueIndexesBackstopRawWriters(t *testing.T) {
	s := openTest(t)
	base := MemorySynthesisRun{ProjectKey: "proj", Day: "d", InputDigest: "x", MaterialSchema: "v1", StartedAt: 1}
	a, b := base, base
	a.ID, a.Status = "a", "running"
	b.ID, b.Status = "b", "running"
	assert.NoErr(t, s.InsertMemorySynthesisRun(a))
	err := s.InsertMemorySynthesisRun(b)
	assert.Err(t, err)
	assert.True(t, strings.Contains(err.Error(), "UNIQUE"))
	// Two failed runs of one day are fine; two succeeded with one digest are not.
	for _, id := range []string{"f1", "f2"} {
		f := base
		f.ID, f.Status = id, "failed"
		assert.NoErr(t, s.InsertMemorySynthesisRun(f))
	}
	s1, s2 := base, base
	s1.ID, s1.Status = "s1", "succeeded"
	s2.ID, s2.Status = "s2", "succeeded"
	assert.NoErr(t, s.InsertMemorySynthesisRun(s1))
	assert.Err(t, s.InsertMemorySynthesisRun(s2))
}

func TestFailStaleMemorySynthesisRuns(t *testing.T) {
	s := openTest(t)
	for _, c := range []struct {
		id      string
		project string
		at      int64
	}{{"old", "p1", 100}, {"fresh", "p2", 900}} {
		in := beginIn(c.id, "d", c.at)
		in.ProjectKey = c.project
		_, _, err := s.BeginMemorySynthesisIfChanged(in)
		assert.NoErr(t, err)
	}
	n, err := s.FailStaleMemorySynthesisRuns(500, 1000, "server restart")
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), n)
	old, _, _ := s.GetMemorySynthesisRun("old")
	assert.Eq(t, "failed", old.Status)
	assert.Eq(t, "server restart", old.Error)
	assert.Eq(t, int64(1000), old.FinishedAt)
	fresh, _, _ := s.GetMemorySynthesisRun("fresh")
	assert.Eq(t, "running", fresh.Status)
}

func TestPruneMemorySynthesisRuns(t *testing.T) {
	s := openTest(t)
	mk := func(id, status string, started, finished int64) {
		assert.NoErr(t, s.InsertMemorySynthesisRun(MemorySynthesisRun{ID: id, ProjectKey: "p" + id, Day: "d", Status: status, InputDigest: id, MaterialSchema: "v1", StartedAt: started, FinishedAt: finished}))
	}
	mk("old-ok", "succeeded", 10, 20)
	mk("old-fail", "failed", 10, 20)
	mk("old-run", "running", 10, 0)
	mk("new-ok", "succeeded", 10, 900)
	n, err := s.PruneMemorySynthesisRuns(500)
	assert.NoErr(t, err)
	assert.Eq(t, int64(2), n)
	rows, _ := s.ListMemorySynthesisRuns(MemorySynthesisRunFilter{Status: "all"})
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	assert.Eq(t, 2, len(ids))
	assert.True(t, strings.Contains(strings.Join(ids, ","), "old-run") && strings.Contains(strings.Join(ids, ","), "new-ok"))
}

func TestLatestSucceededMemorySynthesisIsBeforeDay(t *testing.T) {
	s := openTest(t)
	for i, d := range []string{"2026-10-26", "2026-10-27", "2026-10-28"} {
		assert.NoErr(t, s.InsertMemorySynthesisRun(MemorySynthesisRun{ID: d, ProjectKey: "proj", Day: d, Status: "succeeded", InputDigest: d, MaterialSchema: "v1", StartedAt: int64(i + 1)}))
	}
	r, ok, err := s.LatestSucceededMemorySynthesis("proj", "2026-10-28")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "2026-10-27", r.Day)
	_, ok, _ = s.LatestSucceededMemorySynthesis("proj", "2026-10-26")
	assert.False(t, ok)
	_, ok, _ = s.LatestSucceededMemorySynthesis("other", "2026-10-28")
	assert.False(t, ok)
}

// A database holding rows from before the integrity indexes (duplicate running rows) must
// still open and be normalised by the migration instead of failing the unique index build.
func TestMigrateMemorySynthesisNormalisesLegacyDuplicates(t *testing.T) {
	s := openTest(t)
	_, err := s.db.Exec(`DROP INDEX uq_memory_synthesis_runs_running`)
	assert.NoErr(t, err)
	_, err = s.db.Exec(`DROP INDEX uq_memory_synthesis_runs_succeeded`)
	assert.NoErr(t, err)
	for _, id := range []string{"a", "b"} {
		_, err = s.db.Exec(`INSERT INTO memory_synthesis_runs (id, project_key, day, status, input_digest, material_schema, started_at) VALUES (?, 'p', 'd', 'running', 'x', 'v1', 1)`, id)
		assert.NoErr(t, err)
	}
	for _, id := range []string{"c", "d"} {
		_, err = s.db.Exec(`INSERT INTO memory_synthesis_runs (id, project_key, day, status, input_digest, material_schema, started_at) VALUES (?, 'p2', 'd', 'succeeded', 'x', 'v1', 1)`, id)
		assert.NoErr(t, err)
	}
	assert.NoErr(t, s.migrateMemorySynthesis())
	rows, _ := s.ListMemorySynthesisRuns(MemorySynthesisRunFilter{ProjectKey: "p", Status: "running"})
	assert.Eq(t, 1, len(rows))
	rows, _ = s.ListMemorySynthesisRuns(MemorySynthesisRunFilter{ProjectKey: "p2", Status: "succeeded"})
	assert.Eq(t, 1, len(rows))
}
