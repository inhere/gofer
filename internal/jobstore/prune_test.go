package jobstore

import (
	"sort"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// termJob builds a terminal JobRecord with explicit status/timestamps so the
// retention predicates can be exercised precisely.
func termJob(id, status string, startedAt, endedAt int64) JobRecord {
	r := sampleJob(id, "proj", startedAt)
	r.Status = status
	r.EndedAt = endedAt
	r.UpdatedAt = endedAt
	return r
}

// idsOf returns the ids of a record slice, sorted, for stable comparison.
func remainingIDs(t *testing.T, s *Store) []string {
	t.Helper()
	recs, err := s.ListJobs(ListQuery{Limit: 1000})
	assert.NoErr(t, err)
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

func TestPruneByAge(t *testing.T) {
	s := openTest(t)
	now := int64(1_000_000)

	// old: ended 10 days ago; recent: ended 1 hour ago.
	assert.NoErr(t, s.UpsertJob(termJob("old", "done", now-100000, now-10*24*3600)))
	assert.NoErr(t, s.UpsertJob(termJob("recent", "done", now-50000, now-3600)))

	deleted, dirs, err := s.PruneJobs(RetentionPolicy{MaxAge: 7 * 24 * time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)
	// sampleJob sets ResultDir to "/tmp/results/<id>".
	assert.Eq(t, []string{"/tmp/results/old"}, dirs)
	assert.Eq(t, []string{"recent"}, remainingIDs(t, s))
}

func TestPruneByCount(t *testing.T) {
	s := openTest(t)
	now := int64(2_000_000)

	// Five terminal jobs, started_at ascending: j1 oldest .. j5 newest.
	for i, id := range []string{"j1", "j2", "j3", "j4", "j5"} {
		st := now - int64((5-i)*1000) // j1 earliest, j5 latest
		assert.NoErr(t, s.UpsertJob(termJob(id, "done", st, st)))
	}

	deleted, dirs, err := s.PruneJobs(RetentionPolicy{MaxCount: 2}, now)
	assert.NoErr(t, err)
	// Keep the newest 2 (j4, j5); delete j1, j2, j3.
	assert.Eq(t, 3, deleted)
	assert.Eq(t, 3, len(dirs))
	assert.Eq(t, []string{"j4", "j5"}, remainingIDs(t, s))
}

func TestPruneNeverDeletesLiveJobs(t *testing.T) {
	s := openTest(t)
	now := int64(3_000_000)

	// Live jobs (never pruned), all "ended" long ago via updated_at to prove the
	// age predicate cannot touch them.
	for _, st := range []string{"queued", "running", "pending_interaction"} {
		j := termJob("live-"+st, st, now-100000, 0)
		j.UpdatedAt = now - 100000
		assert.NoErr(t, s.UpsertJob(j))
	}
	// One terminal job old enough to be pruned.
	assert.NoErr(t, s.UpsertJob(termJob("term", "done", now-100000, now-100000)))

	deleted, _, err := s.PruneJobs(RetentionPolicy{MaxAge: time.Hour, MaxCount: 1}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)
	assert.Eq(t, []string{"live-pending_interaction", "live-queued", "live-running"}, remainingIDs(t, s))
}

func TestPruneRemovesInteractions(t *testing.T) {
	s := openTest(t)
	now := int64(4_000_000)

	assert.NoErr(t, s.UpsertJob(termJob("term", "done", now-100000, now-100000)))
	assert.NoErr(t, s.UpsertInteraction(InteractionRecord{
		ID: "i1", JobID: "term", Type: "confirm", Prompt: "ok?", Status: "pending", CreatedAt: now,
	}))
	assert.NoErr(t, s.UpsertInteraction(InteractionRecord{
		ID: "i2", JobID: "term", Type: "confirm", Prompt: "go?", Status: "pending", CreatedAt: now,
	}))
	// A live job's interaction must survive the prune.
	live := termJob("live", "running", now, 0)
	live.UpdatedAt = now
	assert.NoErr(t, s.UpsertJob(live))
	assert.NoErr(t, s.UpsertInteraction(InteractionRecord{
		ID: "k1", JobID: "live", Type: "confirm", Prompt: "?", Status: "pending", CreatedAt: now,
	}))

	deleted, _, err := s.PruneJobs(RetentionPolicy{MaxAge: time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)

	gone, err := s.ListInteractions("term")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(gone))

	kept, err := s.ListInteractions("live")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(kept))
}

func TestPruneReturnsResultDirs(t *testing.T) {
	s := openTest(t)
	now := int64(5_000_000)

	a := termJob("a", "failed", now-100000, now-100000)
	a.ResultDir = "/var/logs/a"
	b := termJob("b", "timeout", now-90000, now-90000)
	b.ResultDir = "/var/logs/b"
	assert.NoErr(t, s.UpsertJob(a))
	assert.NoErr(t, s.UpsertJob(b))

	_, dirs, err := s.PruneJobs(RetentionPolicy{MaxAge: time.Hour}, now)
	assert.NoErr(t, err)
	sort.Strings(dirs)
	assert.Eq(t, []string{"/var/logs/a", "/var/logs/b"}, dirs)
}

func TestPruneZeroPolicyDeletesNothing(t *testing.T) {
	s := openTest(t)
	now := int64(6_000_000)
	assert.NoErr(t, s.UpsertJob(termJob("term", "done", now-100000, now-100000)))

	deleted, dirs, err := s.PruneJobs(RetentionPolicy{}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 0, deleted)
	assert.Eq(t, 0, len(dirs))
	assert.Eq(t, []string{"term"}, remainingIDs(t, s))
}

func TestPruneUsesUpdatedAtWhenEndedAtZero(t *testing.T) {
	s := openTest(t)
	now := int64(7_000_000)

	// ended_at = 0 but updated_at is old -> age predicate uses updated_at.
	j := termJob("stale", "failed", now-100000, 0)
	j.UpdatedAt = now - 100000
	assert.NoErr(t, s.UpsertJob(j))

	deleted, _, err := s.PruneJobs(RetentionPolicy{MaxAge: time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)
	assert.Eq(t, 0, len(remainingIDs(t, s)))
}
