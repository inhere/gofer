package jobstore

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// openTest opens a fresh Store backed by a temp-dir db file and registers its
// Close with the test. A file (not :memory:) is required because WAL is disabled
// for in-memory databases (design §14: tests use a temp db file).
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	assert.NotNil(t, s)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// sampleJob builds a queued JobRecord with a given id/project for tests.
func sampleJob(id, project string, startedAt int64) JobRecord {
	return JobRecord{
		ID:          id,
		ProjectKey:  project,
		Agent:       "claude",
		Runner:      "local",
		Status:      "queued",
		Cwd:         ".",
		ResultDir:   "/tmp/results/" + id,
		RequestJSON: `{"project_key":"` + project + `"}`,
		StartedAt:   startedAt,
	}
}

// TestReconcileOrphanJobs: non-terminal (queued/running) jobs left by a prior serve
// are failed with the reason + ts stamped; terminal jobs are untouched; idempotent.
func TestReconcileOrphanJobs(t *testing.T) {
	s := openTest(t)
	const ts = int64(1_700_000_000)

	running := sampleJob("run1", "proj", 100)
	running.Status = "running"
	running.UpdatedAt = 100
	queued := sampleJob("q1", "proj", 100)
	queued.Status = "queued"
	queued.UpdatedAt = 100
	done := sampleJob("d1", "proj", 100)
	done.Status, done.EndedAt, done.UpdatedAt = "done", 200, 200
	failed := sampleJob("f1", "proj", 100)
	failed.Status, failed.Error, failed.EndedAt, failed.UpdatedAt = "failed", "boom", 200, 200
	for _, j := range []JobRecord{running, queued, done, failed} {
		assert.NoErr(t, s.UpsertJob(j))
	}

	n, err := s.ReconcileOrphanJobs(ts, "orphaned-test")
	assert.NoErr(t, err)
	assert.Eq(t, 2, n)

	// running + queued → failed, with error + ended_at/updated_at stamped to ts.
	for _, id := range []string{"run1", "q1"} {
		rec, ok, gerr := s.GetJob(id)
		assert.NoErr(t, gerr)
		assert.True(t, ok)
		assert.Eq(t, "failed", rec.Status)
		assert.Eq(t, "orphaned-test", rec.Error)
		assert.Eq(t, ts, rec.EndedAt)
		assert.Eq(t, ts, rec.UpdatedAt)
	}
	// terminal jobs are untouched (status / error / ended_at preserved).
	d, _, _ := s.GetJob("d1")
	assert.Eq(t, "done", d.Status)
	assert.Eq(t, int64(200), d.EndedAt)
	f, _, _ := s.GetJob("f1")
	assert.Eq(t, "failed", f.Status)
	assert.Eq(t, "boom", f.Error) // existing failure reason not clobbered
	assert.Eq(t, int64(200), f.EndedAt)

	// Idempotent: a second pass finds nothing non-terminal.
	n2, err := s.ReconcileOrphanJobs(ts+1, "orphaned-test-2")
	assert.NoErr(t, err)
	assert.Eq(t, 0, n2)
}

func TestOpenCreatesSchemaIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gofer.db")
	s, err := Open(path)
	assert.NoErr(t, err)
	assert.NoErr(t, s.Close())

	// Re-opening the same file must not fail (IF NOT EXISTS DDL) and the data
	// must survive across opens (durability of the file-backed store).
	s2, err := Open(path)
	assert.NoErr(t, err)
	defer s2.Close()
	assert.NoErr(t, s2.UpsertJob(sampleJob("j1", "proj", 100)))

	s3, err := Open(path)
	assert.NoErr(t, err)
	defer s3.Close()
	_, ok, err := s3.GetJob("j1")
	assert.NoErr(t, err)
	assert.True(t, ok)
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	_, err := Open("")
	assert.Err(t, err)
}

func TestUpsertGetRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleJob("job-1", "alpha", 1000)
	in.WorkerID = "w7"
	in.Error = ""
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("job-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "job-1", got.ID)
	assert.Eq(t, "alpha", got.ProjectKey)
	assert.Eq(t, "claude", got.Agent)
	assert.Eq(t, "local", got.Runner)
	assert.Eq(t, "w7", got.WorkerID)
	assert.Eq(t, "queued", got.Status)
	assert.Eq(t, ".", got.Cwd)
	assert.Eq(t, "/tmp/results/job-1", got.ResultDir)
	assert.Eq(t, `{"project_key":"alpha"}`, got.RequestJSON)
	assert.Eq(t, int64(1000), got.StartedAt)
	// UpdatedAt defaults to StartedAt when the caller leaves it zero.
	assert.Eq(t, int64(1000), got.UpdatedAt)
	assert.Eq(t, int64(0), got.EndedAt)
}

// TestUpsertGetSessionIDRoundTrip proves the session_id column round-trips:
// a set value survives upsert+read, and an empty one reads back as "" (the
// COALESCE in selectCols guards a NULL into the zero value).
func TestUpsertGetSessionIDRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleJob("sess-1", "alpha", 1000)
	in.SessionID = "67cc4d00-1111-4abc-8def-0123456789ab"
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("sess-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "67cc4d00-1111-4abc-8def-0123456789ab", got.SessionID)

	// A job with no session_id reads back as "" (not an error, not NULL).
	none := sampleJob("sess-none", "alpha", 1001)
	assert.NoErr(t, s.UpsertJob(none))
	got2, ok, err := s.GetJob("sess-none")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", got2.SessionID)
}

// TestUpsertGetChannelClientRoundTrip proves the provenance columns channel /
// client round-trip through upsert+read, and absent values COALESCE to "".
func TestUpsertGetChannelClientRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleJob("prov-1", "alpha", 1000)
	in.Channel = "cli"
	in.Client = "dev-container-7"
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("prov-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "cli", got.Channel)
	assert.Eq(t, "dev-container-7", got.Client)

	none := sampleJob("prov-none", "alpha", 1001)
	assert.NoErr(t, s.UpsertJob(none))
	got2, _, err := s.GetJob("prov-none")
	assert.NoErr(t, err)
	assert.Eq(t, "", got2.Channel)
	assert.Eq(t, "", got2.Client)
}

// TestUpsertGetInteractiveRoundTrip proves the WEB-03 interactive flag survives
// INSERT/SELECT and defaults to false for ordinary jobs.
func TestUpsertGetInteractiveRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleJob("pty-1", "alpha", 1000)
	in.Interactive = true
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("pty-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, got.Interactive)

	plain := sampleJob("plain-1", "alpha", 1001)
	assert.NoErr(t, s.UpsertJob(plain))
	gotPlain, _, err := s.GetJob("plain-1")
	assert.NoErr(t, err)
	assert.False(t, gotPlain.Interactive)

	list, err := s.ListJobs(ListQuery{Limit: 10})
	assert.NoErr(t, err)
	byID := map[string]JobRecord{}
	for _, rec := range list {
		byID[rec.ID] = rec
	}
	assert.True(t, byID["pty-1"].Interactive)
	assert.False(t, byID["plain-1"].Interactive)
}

// TestUpsertGetOriginAgentRoundTrip proves the supervisor-routing owner columns
// origin_agent / escalate_to round-trip through upsert+read, and absent values
// COALESCE to "" (supervisor-routing P1.1).
func TestUpsertGetOriginAgentRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleJob("own-1", "alpha", 2000)
	in.OriginAgent = "agt_owner_x"
	in.EscalateTo = "role-one:supervisor"
	in.Role = "supervisor"
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("own-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "agt_owner_x", got.OriginAgent)
	assert.Eq(t, "role-one:supervisor", got.EscalateTo)
	assert.Eq(t, "supervisor", got.Role)

	none := sampleJob("own-none", "alpha", 2001)
	assert.NoErr(t, s.UpsertJob(none))
	got2, _, err := s.GetJob("own-none")
	assert.NoErr(t, err)
	assert.Eq(t, "", got2.OriginAgent)
	assert.Eq(t, "", got2.EscalateTo)
	assert.Eq(t, "", got2.Role)
}

func TestUpsertGetPlanIDRoundTripAndFilter(t *testing.T) {
	s := openTest(t)

	in := sampleJob("plan-job", "alpha", 2000)
	in.PlanID = "plan-x"
	assert.NoErr(t, s.UpsertJob(in))

	plain := sampleJob("plain-job", "alpha", 2001)
	assert.NoErr(t, s.UpsertJob(plain))

	got, ok, err := s.GetJob("plan-job")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "plan-x", got.PlanID)

	byPlan, err := s.ListJobs(ListQuery{Plan: "plan-x"})
	assert.NoErr(t, err)
	assert.Len(t, byPlan, 1)
	assert.Eq(t, "plan-job", byPlan[0].ID)

	none, err := s.ListJobs(ListQuery{Plan: "plan-none"})
	assert.NoErr(t, err)
	assert.Len(t, none, 0)

	gotPlain, _, err := s.GetJob("plain-job")
	assert.NoErr(t, err)
	assert.Eq(t, "", gotPlain.PlanID)
}

func TestGetJobMissingReturnsFalseNoError(t *testing.T) {
	s := openTest(t)
	got, ok, err := s.GetJob("nope")
	assert.NoErr(t, err)
	assert.False(t, ok)
	assert.Eq(t, "", got.ID)
}

func TestUpsertEmptyIDRejected(t *testing.T) {
	s := openTest(t)
	assert.Err(t, s.UpsertJob(JobRecord{ProjectKey: "p", Agent: "a", Runner: "local", Status: "queued", ResultDir: "/d"}))
}

// TestUpsertIsCreateThenUpdate proves create+finish are two upserts on ONE row
// (the jobs.jsonl design appended two lines per job); the latest snapshot wins
// and the index keeps a single row.
func TestUpsertIsCreateThenUpdate(t *testing.T) {
	s := openTest(t)

	create := sampleJob("j", "p", 500)
	assert.NoErr(t, s.UpsertJob(create))

	finish := create
	finish.Status = "done"
	finish.ExitCode = 0
	finish.EndedAt = 800
	finish.UpdatedAt = 800
	assert.NoErr(t, s.UpsertJob(finish))

	got, ok, err := s.GetJob("j")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "done", got.Status)
	assert.Eq(t, int64(800), got.EndedAt)
	assert.Eq(t, int64(800), got.UpdatedAt)

	all, err := s.ListJobs(ListQuery{})
	assert.NoErr(t, err)
	assert.Len(t, all, 1)
}

func TestListFilterOrderPaginate(t *testing.T) {
	s := openTest(t)

	// Two projects, mixed statuses and started_at values.
	assert.NoErr(t, s.UpsertJob(withStatus(sampleJob("a", "alpha", 100), "done")))
	assert.NoErr(t, s.UpsertJob(withStatus(sampleJob("b", "alpha", 300), "running")))
	assert.NoErr(t, s.UpsertJob(withStatus(sampleJob("c", "beta", 200), "done")))
	assert.NoErr(t, s.UpsertJob(withStatus(sampleJob("d", "beta", 400), "failed")))

	// Default: all jobs, newest first.
	all, err := s.ListJobs(ListQuery{})
	assert.NoErr(t, err)
	assert.Len(t, all, 4)
	assert.Eq(t, "d", all[0].ID) // started_at 400
	assert.Eq(t, "b", all[1].ID) // 300
	assert.Eq(t, "c", all[2].ID) // 200
	assert.Eq(t, "a", all[3].ID) // 100

	// Project filter.
	alpha, err := s.ListJobs(ListQuery{Project: "alpha"})
	assert.NoErr(t, err)
	assert.Len(t, alpha, 2)
	assert.Eq(t, "b", alpha[0].ID)
	assert.Eq(t, "a", alpha[1].ID)

	// Status filter.
	done, err := s.ListJobs(ListQuery{Status: "done"})
	assert.NoErr(t, err)
	assert.Len(t, done, 2)
	for _, r := range done {
		assert.Eq(t, "done", r.Status)
	}

	// Since filter (started_at >= 300).
	recent, err := s.ListJobs(ListQuery{Since: 300})
	assert.NoErr(t, err)
	assert.Len(t, recent, 2)
	assert.Eq(t, "d", recent[0].ID)
	assert.Eq(t, "b", recent[1].ID)

	// Limit + offset pagination over the newest-first order.
	page1, err := s.ListJobs(ListQuery{Limit: 2})
	assert.NoErr(t, err)
	assert.Len(t, page1, 2)
	assert.Eq(t, "d", page1[0].ID)
	assert.Eq(t, "b", page1[1].ID)

	page2, err := s.ListJobs(ListQuery{Limit: 2, Offset: 2})
	assert.NoErr(t, err)
	assert.Len(t, page2, 2)
	assert.Eq(t, "c", page2[0].ID)
	assert.Eq(t, "a", page2[1].ID)

	// Combined project + status filter, no matches.
	none, err := s.ListJobs(ListQuery{Project: "alpha", Status: "failed"})
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
}

func TestListEmptyStore(t *testing.T) {
	s := openTest(t)
	out, err := s.ListJobs(ListQuery{})
	assert.NoErr(t, err)
	assert.Len(t, out, 0)
}

// TestConcurrentUpserts exercises the WAL + busy_timeout concurrency contract:
// many goroutines upsert distinct rows while several hammer a single "hot" row.
// None must error and the final row count must equal the distinct id count.
func TestConcurrentUpserts(t *testing.T) {
	s := openTest(t)

	const (
		writers     = 16
		perWriter   = 40
		hotUpdaters = 8
		hotUpdates  = 50
	)

	errCh := make(chan error, writers*perWriter+hotUpdaters*hotUpdates)
	var wg sync.WaitGroup

	// Distinct-row writers: writers*perWriter unique jobs.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("w%02d-%03d", w, i)
				if err := s.UpsertJob(sampleJob(id, fmt.Sprintf("proj%d", w%3), int64(w*1000+i))); err != nil {
					errCh <- err
				}
			}
		}(w)
	}

	// Hot-row updaters: all upsert the SAME id concurrently (status churn).
	hot := sampleJob("hot", "proj0", 1)
	for u := 0; u < hotUpdaters; u++ {
		wg.Add(1)
		go func(u int) {
			defer wg.Done()
			for i := 0; i < hotUpdates; i++ {
				rec := hot
				rec.Status = fmt.Sprintf("s%d-%d", u, i)
				rec.UpdatedAt = int64(u*1000 + i)
				if err := s.UpsertJob(rec); err != nil {
					errCh <- err
				}
			}
		}(u)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		assert.NoErr(t, err)
	}

	// distinct rows = writers*perWriter unique jobs + 1 hot row.
	all, err := s.ListJobs(ListQuery{Limit: writers*perWriter + 10})
	assert.NoErr(t, err)
	assert.Eq(t, writers*perWriter+1, len(all))

	// The hot row exists and survived the churn (one of the written statuses).
	hotRec, ok, err := s.GetJob("hot")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.NotEmpty(t, hotRec.Status)
}

// withStatus returns a copy of rec with Status set (test helper).
func withStatus(rec JobRecord, status string) JobRecord {
	rec.Status = status
	return rec
}

// TestCountActiveJobsByRole: only ACTIVE (queued/running/pending_interaction) jobs of
// the queried role count; terminal jobs and other/empty roles are excluded (P4b
// supervisor reconciler replica signal).
func TestCountActiveJobsByRole(t *testing.T) {
	s := openTest(t)
	mk := func(id, role, status string) JobRecord {
		j := sampleJob(id, "proj", 100)
		j.Role, j.Status = role, status
		return j
	}
	for _, j := range []JobRecord{
		mk("s-queued", "supervisor", "queued"),
		mk("s-running", "supervisor", "running"),
		mk("s-pending", "supervisor", "pending_interaction"),
		mk("s-done", "supervisor", "done"),     // terminal — excluded
		mk("s-failed", "supervisor", "failed"), // terminal — excluded
		mk("d-running", "", "running"),         // no role — excluded
		mk("r-running", "reviewer", "running"), // other role — excluded
	} {
		assert.NoErr(t, s.UpsertJob(j))
	}

	n, err := s.CountActiveJobsByRole("supervisor")
	assert.NoErr(t, err)
	assert.Eq(t, 3, n) // queued + running + pending_interaction only

	n, err = s.CountActiveJobsByRole("nobody")
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
}
