package jobstore

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func sampleEnroll(id, source string, at int64) WorkerEnrollment {
	return WorkerEnrollment{ID: id, SecretHash: "h-" + id, PairingCode: "ABCD2345", Source: source,
		CreatedAt: at, DecideUntil: at + 120}
}

// TestMigrateAddsWorkerEnrollmentsToOldDB: a database that predates the table (only
// an unrelated old table exists) gains worker_enrollments with its indexes on Open
// and the table is usable; a second Open is a no-op.
func TestMigrateAddsWorkerEnrollmentsToOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-enroll.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL,
	  target_id TEXT NOT NULL, actor TEXT NOT NULL DEFAULT '', at INTEGER NOT NULL)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	for _, c := range []string{"id", "state", "secret_hash", "pairing_code", "code_failures", "secret_failures",
		"requested_id", "worker_id", "labels_json", "projects_json", "host_json", "source", "token_fp",
		"created_at", "decide_until", "collect_until", "decided_at", "decided_by", "reason", "collected_at"} {
		assert.True(t, tableHasColumn(t, s, "worker_enrollments", c), c)
	}
	assert.True(t, indexExists(t, s, "idx_worker_enrollments_state"))
	_, err = s.InsertWorkerEnrollment(sampleEnroll("we_1", "1.1.1.1", 100), 0)
	assert.NoErr(t, err)
	assert.NoErr(t, s.Close())

	s2, err := Open(path)
	assert.NoErr(t, err)
	defer s2.Close()
	got, err := s2.GetWorkerEnrollment("we_1")
	assert.NoErr(t, err)
	assert.Eq(t, EnrollPending, got.State)
}

func TestWorkerEnrollmentInsertSupersedeAndCap(t *testing.T) {
	s := openTest(t)
	var changes []Change
	s.SetChangeHook(func(c Change) { changes = append(changes, c) })

	_, err := s.InsertWorkerEnrollment(sampleEnroll("we_a", "src1", 100), 2)
	assert.NoErr(t, err)
	sup, err := s.InsertWorkerEnrollment(sampleEnroll("we_b", "src1", 101), 2)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"we_a"}, sup)
	a, _ := s.GetWorkerEnrollment("we_a")
	assert.Eq(t, EnrollSuperseded, a.State)

	_, err = s.InsertWorkerEnrollment(sampleEnroll("we_c", "src2", 102), 2)
	assert.NoErr(t, err)
	// cap reached (b, c pending): a new source is refused and src1's pending stays pending.
	_, err = s.InsertWorkerEnrollment(sampleEnroll("we_d", "src3", 103), 2)
	assert.Eq(t, ErrWorkerEnrollmentCap, err)
	_, err = s.InsertWorkerEnrollment(sampleEnroll("we_e", "src1", 104), 2)
	assert.NoErr(t, err) // supersedes b first, so the count is 1 + new
	b, _ := s.GetWorkerEnrollment("we_b")
	assert.Eq(t, EnrollSuperseded, b.State)
	_, err = s.GetWorkerEnrollment("we_d")
	assert.Eq(t, ErrWorkerEnrollmentNotFound, err)

	// an expired-but-unswept pending row does not count toward the cap.
	_, err = s.InsertWorkerEnrollment(sampleEnroll("we_f", "src4", 100+500), 2)
	assert.NoErr(t, err)
	assert.True(t, len(changes) > 0)
	for _, c := range changes {
		assert.Eq(t, ChangeWorkerEnrollment, c.Kind)
	}
}

func TestWorkerEnrollmentClaimIsConditional(t *testing.T) {
	s := openTest(t)
	_, err := s.InsertWorkerEnrollment(sampleEnroll("we_1", "s", 100), 0)
	assert.NoErr(t, err)

	// after decide_until the claim loses; the row stays pending.
	ok, err := s.ClaimWorkerEnrollment(WorkerEnrollmentClaim{ID: "we_1", Now: 221, WorkerID: "w"})
	assert.NoErr(t, err)
	assert.False(t, ok)

	claim := WorkerEnrollmentClaim{ID: "we_1", Now: 150, WorkerID: "w1", LabelsJSON: `["a"]`, ProjectsJSON: `["p"]`, TokenFP: "fp", DecidedBy: "admin"}
	ok, err = s.ClaimWorkerEnrollment(claim)
	assert.NoErr(t, err)
	assert.True(t, ok)
	ok, _ = s.ClaimWorkerEnrollment(claim) // second claimant loses
	assert.False(t, ok)

	// an expiry that only targets pending cannot touch an accepting row.
	ok, _ = s.TransitionWorkerEnrollment("we_1", []string{EnrollPending}, EnrollExpired, 200, "system", "")
	assert.False(t, ok)

	ok, err = s.FinishWorkerEnrollmentAccept("we_1", 210)
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _ := s.GetWorkerEnrollment("we_1")
	assert.Eq(t, EnrollAccepted, got.State)
	assert.Eq(t, "w1", got.WorkerID)
	assert.Eq(t, "fp", got.TokenFP)
	assert.Eq(t, int64(210), got.CollectUntil)
	assert.Eq(t, "admin", got.DecidedBy)

	f, err := s.FindAcceptedWorkerEnrollment("w1")
	assert.NoErr(t, err)
	assert.Eq(t, "we_1", f.ID)

	ok, _ = s.TransitionWorkerEnrollment("we_1", []string{EnrollAccepted}, EnrollCollected, 205, "", "")
	assert.True(t, ok)
	got, _ = s.GetWorkerEnrollment("we_1")
	assert.Eq(t, int64(205), got.CollectedAt)
	_, err = s.FindAcceptedWorkerEnrollment("w1")
	assert.Eq(t, ErrWorkerEnrollmentNotFound, err)
}

func TestWorkerEnrollmentRevertClearsDecision(t *testing.T) {
	s := openTest(t)
	_, _ = s.InsertWorkerEnrollment(sampleEnroll("we_1", "s", 100), 0)
	ok, _ := s.ClaimWorkerEnrollment(WorkerEnrollmentClaim{ID: "we_1", Now: 110, WorkerID: "w", TokenFP: "fp", DecidedBy: "me"})
	assert.True(t, ok)
	ok, err := s.RevertWorkerEnrollmentAccept("we_1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _ := s.GetWorkerEnrollment("we_1")
	assert.Eq(t, EnrollPending, got.State)
	assert.Eq(t, "", got.WorkerID)
	assert.Eq(t, "", got.TokenFP)
	assert.Eq(t, "", got.DecidedBy)
}

func TestWorkerEnrollmentCountersAreAtomic(t *testing.T) {
	s := openTest(t)
	_, _ = s.InsertWorkerEnrollment(sampleEnroll("we_1", "s", 100), 0)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.BumpWorkerEnrollmentCodeFailures("we_1")
			_, _ = s.BumpWorkerEnrollmentSecretFailures("we_1")
		}()
	}
	wg.Wait()
	got, _ := s.GetWorkerEnrollment("we_1")
	assert.Eq(t, 8, got.CodeFailures)
	assert.Eq(t, 8, got.SecretFailures)
	_, err := s.BumpWorkerEnrollmentCodeFailures("nope")
	assert.Eq(t, ErrWorkerEnrollmentNotFound, err)
}

func TestWorkerEnrollmentListAndPrune(t *testing.T) {
	s := openTest(t)
	for i, id := range []string{"we_1", "we_2", "we_3"} {
		_, _ = s.InsertWorkerEnrollment(sampleEnroll(id, id, int64(100+i)), 0)
	}
	_, _ = s.TransitionWorkerEnrollment("we_1", []string{EnrollPending}, EnrollRejected, 120, "admin", "no")
	l, err := s.ListWorkerEnrollmentsAdmin(0)
	assert.NoErr(t, err)
	assert.Len(t, l, 3)
	assert.Eq(t, "we_3", l[0].ID) // newest first
	l, _ = s.ListWorkerEnrollmentsAdmin(1000)
	assert.Len(t, l, 2) // ended row is too old, open ones always listed
	byState, _ := s.ListWorkerEnrollmentsByState(EnrollRejected)
	assert.Len(t, byState, 1)
	assert.Eq(t, "no", byState[0].Reason)

	n, err := s.DeleteEndedWorkerEnrollmentsBefore(1000)
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)
	l, _ = s.ListWorkerEnrollmentsAdmin(0)
	assert.Len(t, l, 2)
}
