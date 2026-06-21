package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// TestJobEventsTableExists proves Open creates the job_events table (E13). PRAGMA
// table_info returns rows only for an existing table, so a non-empty column set
// confirms the DDL ran.
func TestJobEventsTableExists(t *testing.T) {
	s := openTest(t)
	cols, err := s.tableColumns("job_events")
	assert.NoErr(t, err)
	for _, c := range []string{"seq", "job_id", "type", "detail_json", "at"} {
		if _, ok := cols[c]; !ok {
			t.Fatalf("job_events missing column %q (cols=%v)", c, cols)
		}
	}
}

func TestInsertJobEventReturnsSeq(t *testing.T) {
	s := openTest(t)
	seq1, err := s.InsertJobEvent(JobEvent{JobID: "j1", Type: "job.submitted", Detail: `{"a":1}`, At: 100})
	assert.NoErr(t, err)
	seq2, err := s.InsertJobEvent(JobEvent{JobID: "j1", Type: "job.running", At: 101})
	assert.NoErr(t, err)
	// AUTOINCREMENT seq is monotonic across inserts.
	if seq2 <= seq1 {
		t.Fatalf("expected monotonic seq, got %d then %d", seq1, seq2)
	}
}

func TestInsertJobEventValidation(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertJobEvent(JobEvent{Type: "x", At: 1}); err == nil {
		t.Fatalf("expected error on empty job id")
	}
	if _, err := s.InsertJobEvent(JobEvent{JobID: "j1", At: 1}); err == nil {
		t.Fatalf("expected error on empty type")
	}
}

func TestListJobEventsOrderAndSince(t *testing.T) {
	s := openTest(t)

	seq1, err := s.InsertJobEvent(JobEvent{JobID: "j1", Type: "job.submitted", Detail: `{"k":1}`, At: 100})
	assert.NoErr(t, err)
	_, err = s.InsertJobEvent(JobEvent{JobID: "j1", Type: "job.running", At: 101})
	assert.NoErr(t, err)
	_, err = s.InsertJobEvent(JobEvent{JobID: "j1", Type: "job.terminal", Detail: `{"status":"done"}`, At: 102})
	assert.NoErr(t, err)

	// (j1, 0): all three, seq ASC.
	all, err := s.ListJobEvents("j1", 0)
	assert.NoErr(t, err)
	assert.Len(t, all, 3)
	assert.Eq(t, "job.submitted", all[0].Type)
	assert.Eq(t, "job.running", all[1].Type)
	assert.Eq(t, "job.terminal", all[2].Type)
	// seq strictly increasing.
	if !(all[0].Seq < all[1].Seq && all[1].Seq < all[2].Seq) {
		t.Fatalf("events not in seq order: %d %d %d", all[0].Seq, all[1].Seq, all[2].Seq)
	}
	// detail round-trips (empty stays empty).
	assert.Eq(t, `{"k":1}`, all[0].Detail)
	assert.Eq(t, "", all[1].Detail)
	assert.Eq(t, int64(100), all[0].At)

	// (j1, seq1): only the two after the first.
	since, err := s.ListJobEvents("j1", seq1)
	assert.NoErr(t, err)
	assert.Len(t, since, 2)
	assert.Eq(t, "job.running", since[0].Type)
	assert.Eq(t, "job.terminal", since[1].Type)
}

func TestListJobEventsJobIsolation(t *testing.T) {
	s := openTest(t)
	_, err := s.InsertJobEvent(JobEvent{JobID: "j1", Type: "job.submitted", At: 1})
	assert.NoErr(t, err)
	_, err = s.InsertJobEvent(JobEvent{JobID: "j2", Type: "job.submitted", At: 2})
	assert.NoErr(t, err)

	j1, err := s.ListJobEvents("j1", 0)
	assert.NoErr(t, err)
	assert.Len(t, j1, 1)
	assert.Eq(t, "j1", j1[0].JobID)

	none, err := s.ListJobEvents("absent", 0)
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
}

// TestPruneDeletesJobEvents proves a pruned job's events are removed with the
// job row (E13 prune connection).
func TestPruneDeletesJobEvents(t *testing.T) {
	s := openTest(t)
	now := int64(1_000_000)

	// One old terminal job (pruned by age) with events, one recent (kept) with events.
	assert.NoErr(t, s.UpsertJob(termJob("old", "done", now-100000, now-10*24*3600)))
	assert.NoErr(t, s.UpsertJob(termJob("recent", "done", now-50000, now-3600)))
	_, err := s.InsertJobEvent(JobEvent{JobID: "old", Type: "job.submitted", At: now - 100000})
	assert.NoErr(t, err)
	_, err = s.InsertJobEvent(JobEvent{JobID: "old", Type: "job.terminal", At: now - 10*24*3600})
	assert.NoErr(t, err)
	_, err = s.InsertJobEvent(JobEvent{JobID: "recent", Type: "job.submitted", At: now - 50000})
	assert.NoErr(t, err)

	deleted, _, err := s.PruneJobs(RetentionPolicy{MaxAge: 7 * 24 * time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)

	// old's events are gone; recent's survive.
	oldEvents, err := s.ListJobEvents("old", 0)
	assert.NoErr(t, err)
	assert.Len(t, oldEvents, 0)
	recentEvents, err := s.ListJobEvents("recent", 0)
	assert.NoErr(t, err)
	assert.Len(t, recentEvents, 1)
}
