package jobstore

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// samplePtySession builds an open pty session with a given id/job for tests.
func samplePtySession(id, jobID string, startedAt int64) PtySessionRecord {
	return PtySessionRecord{
		PtySessionID: id,
		JobID:        jobID,
		WorkerID:     "w1",
		InstanceID:   "i1",
		Owner:        "caller-1",
		State:        "open",
		Cols:         120,
		Rows:         40,
		RecordingURI: "/tmp/results/" + jobID + "/pty.cast",
		Encrypted:    1,
		StartedAt:    startedAt,
	}
}

// Upsert(open) then Upsert(closed) fold onto one row; Get reads it back.
func TestPtySessionUpsertGet(t *testing.T) {
	s := openTest(t)
	rec := samplePtySession("ps1", "job1", 1000)
	assert.NoErr(t, s.UpsertPtySession(rec))

	got, ok, err := s.GetPtySessionByJob("job1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "ps1", got.PtySessionID)
	assert.Eq(t, "open", got.State)
	assert.Eq(t, int64(0), got.EndedAt) // still open
	assert.Eq(t, 1, got.Encrypted)
	assert.Eq(t, 120, got.Cols)
	assert.Eq(t, 40, got.Rows)
	assert.Eq(t, "caller-1", got.Owner)
	assert.Eq(t, "/tmp/results/job1/pty.cast", got.RecordingURI)

	// finalize: closed snapshot overwrites the same PK.
	rec.State = "closed"
	rec.EndedAt = 2000
	rec.BytesIn = 42
	rec.BytesOut = 1024
	assert.NoErr(t, s.UpsertPtySession(rec))

	got2, ok, err := s.GetPtySessionByJob("job1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "closed", got2.State)
	assert.Eq(t, int64(2000), got2.EndedAt)
	assert.Eq(t, int64(42), got2.BytesIn)
	assert.Eq(t, int64(1024), got2.BytesOut)

	// unknown job -> (zero, false, nil).
	_, ok, err = s.GetPtySessionByJob("nope")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// Encrypted left zero defaults to 2 (SR301 avoid-0).
func TestPtySessionEncryptedDefault(t *testing.T) {
	s := openTest(t)
	rec := samplePtySession("ps1", "job1", 1000)
	rec.Encrypted = 0
	assert.NoErr(t, s.UpsertPtySession(rec))
	got, ok, _ := s.GetPtySessionByJob("job1")
	assert.True(t, ok)
	assert.Eq(t, 2, got.Encrypted)
}

// GetPtySessionByJob returns the most-recently-started session for the job.
func TestPtySessionGetMostRecent(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertPtySession(samplePtySession("ps-old", "job1", 1000)))
	assert.NoErr(t, s.UpsertPtySession(samplePtySession("ps-new", "job1", 2000)))
	got, ok, _ := s.GetPtySessionByJob("job1")
	assert.True(t, ok)
	assert.Eq(t, "ps-new", got.PtySessionID)
}

// Empty id / job id are rejected.
func TestPtySessionUpsertValidation(t *testing.T) {
	s := openTest(t)
	assert.Err(t, s.UpsertPtySession(PtySessionRecord{JobID: "j", State: "open", StartedAt: 1}))
	assert.Err(t, s.UpsertPtySession(PtySessionRecord{PtySessionID: "p", State: "open", StartedAt: 1}))
}

// Reopening the same db file re-runs applySchema (IF NOT EXISTS) without error and
// keeps the data — a pre-existing db auto-gains the new table on Open (migration
// idempotency).
func TestPtySessionSchemaIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gofer.db")
	s1, err := Open(path)
	assert.NoErr(t, err)
	assert.NoErr(t, s1.UpsertPtySession(samplePtySession("ps1", "job1", 1000)))
	assert.NoErr(t, s1.Close())

	s2, err := Open(path)
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	got, ok, err := s2.GetPtySessionByJob("job1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "ps1", got.PtySessionID)
}

// ExpireCastRecordings clears only expired closed sessions' recording, retaining
// the row + audit metadata, and returns the deleted uris.
func TestExpireCastRecordings(t *testing.T) {
	s := openTest(t)
	const now = int64(1_000_000)
	const ttl = int64(3600) // 1h

	expired := samplePtySession("ps-exp", "job-exp", now-10000)
	expired.State = "closed"
	expired.EndedAt = now - 7200 // ended 2h ago -> expired
	assert.NoErr(t, s.UpsertPtySession(expired))

	fresh := samplePtySession("ps-fresh", "job-fresh", now-200)
	fresh.State = "closed"
	fresh.EndedAt = now - 60 // ended 1min ago -> not expired
	assert.NoErr(t, s.UpsertPtySession(fresh))

	open := samplePtySession("ps-open", "job-open", now-50) // never ended
	assert.NoErr(t, s.UpsertPtySession(open))

	uris, err := s.ExpireCastRecordings(now, ttl)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"/tmp/results/job-exp/pty.cast"}, uris)

	// expired row RETAINED, uri cleared, encrypted=2, audit metadata kept.
	got, ok, _ := s.GetPtySessionByJob("job-exp")
	assert.True(t, ok)
	assert.Eq(t, "", got.RecordingURI)
	assert.Eq(t, 2, got.Encrypted)
	assert.Eq(t, "closed", got.State)
	assert.Eq(t, int64(now-7200), got.EndedAt)

	// fresh + open untouched.
	gf, ok, _ := s.GetPtySessionByJob("job-fresh")
	assert.True(t, ok)
	assert.Eq(t, "/tmp/results/job-fresh/pty.cast", gf.RecordingURI)
	go2, ok, _ := s.GetPtySessionByJob("job-open")
	assert.True(t, ok)
	assert.Eq(t, "/tmp/results/job-open/pty.cast", go2.RecordingURI)

	// idempotent: a second sweep finds nothing (row already cleared).
	uris2, err := s.ExpireCastRecordings(now, ttl)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(uris2))
}

// A non-positive TTL prunes nothing.
func TestExpireCastRecordingsDisabled(t *testing.T) {
	s := openTest(t)
	const now = int64(1_000_000)
	rec := samplePtySession("ps1", "job1", now-10000)
	rec.State = "closed"
	rec.EndedAt = now - 7200
	assert.NoErr(t, s.UpsertPtySession(rec))

	uris, err := s.ExpireCastRecordings(now, 0)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(uris))
	got, ok, _ := s.GetPtySessionByJob("job1")
	assert.True(t, ok)
	assert.Eq(t, "/tmp/results/job1/pty.cast", got.RecordingURI) // untouched
}

// PruneJobs drops a pruned job's pty_sessions in the same transaction.
func TestPruneJobsDeletesPtySessions(t *testing.T) {
	s := openTest(t)
	const now = int64(1_000_000)
	assert.NoErr(t, s.UpsertJob(termJob("job1", "done", now-100000, now-10*24*3600)))
	assert.NoErr(t, s.UpsertPtySession(samplePtySession("ps1", "job1", now-100000)))

	deleted, _, err := s.PruneJobs(RetentionPolicy{MaxAge: 7 * 24 * time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)

	_, ok, err := s.GetPtySessionByJob("job1")
	assert.NoErr(t, err)
	assert.False(t, ok) // session row gone with the job

	// regime idempotency: after job prune, a cast sweep is a no-op (no rows).
	uris, err := s.ExpireCastRecordings(now, 3600)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(uris))
}

// PruneWorkflows drops the pty_sessions of every pruned step-job in the same tx.
func TestPruneWorkflowsDeletesPtySessions(t *testing.T) {
	s := openTest(t)
	const now = int64(1_000_000)

	assert.NoErr(t, s.InsertWorkflow(Workflow{
		ID: "wf-old", Status: WorkflowDone, CurrentStep: 2, StepAttempt: 1, TotalSteps: 1,
		SpecJSON: "{}", CreatedAt: 1, UpdatedAt: 100, // very old
	}))
	j := sampleJob("wf-step1", "p", 100)
	j.Status = "done"
	j.WorkflowID = "wf-old"
	j.StepIndex = 1
	assert.NoErr(t, s.UpsertJob(j))
	assert.NoErr(t, s.UpsertPtySession(samplePtySession("ps-step1", "wf-step1", 100)))

	deleted, _, err := s.PruneWorkflows(WorkflowRetentionPolicy{MaxAge: 24 * time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)

	_, ok, err := s.GetPtySessionByJob("wf-step1")
	assert.NoErr(t, err)
	assert.False(t, ok) // step-job's session row gone with the workflow
}
