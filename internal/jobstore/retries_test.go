package jobstore

import (
	"regexp"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// retryIDRe is the R2 short-id shape (XFER-02 decision 5: `<2 letters>-<8hex>`).
var retryIDRe = regexp.MustCompile(`^rt-[0-9a-f]{8}$`)

// TestRetryRowRoundTrip: a job_retries row survives a round trip with every
// column readable, and a job with no retries reads back empty (not an error).
func TestRetryRowRoundTrip(t *testing.T) {
	s := openTest(t)
	rec := RetryRecord{
		ID:          NewRetryID(),
		SourceJobID: "job-1",
		Attempt:     2,
		RequestJSON: `{"project_key":"self","agent":"exec","retry":{"max_attempts":3}}`,
		Reason:      "exit_code=7",
		NextRunAt:   120,
		CreatedAt:   100,
	}
	if !retryIDRe.MatchString(rec.ID) {
		t.Fatalf("NewRetryID() = %q, want rt-<8hex>", rec.ID)
	}
	assert.NoErr(t, s.InsertRetry(rec))

	rows, err := s.ListRetriesByJob("job-1")
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
	got := rows[0]
	if got.ID != rec.ID || got.SourceJobID != "job-1" || got.Attempt != 2 {
		t.Fatalf("row identity = %+v, want the inserted one", got)
	}
	if got.RequestJSON != rec.RequestJSON || got.Reason != "exit_code=7" ||
		got.NextRunAt != 120 || got.CreatedAt != 100 {
		t.Fatalf("row payload = %+v, want the inserted values", got)
	}
	if got.State != RetryPending || got.LeaseUntil != 0 || got.NewJobID != "" {
		t.Fatalf("row state = %+v, want a fresh pending row (no lease, no new job)", got)
	}

	none, err := s.ListRetriesByJob("job-none")
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
}

// TestClaimDueRetriesLease: the claim is a lease. A due row is handed out once
// (state=claimed, lease_until=now+lease), a second claim at the same instant gets
// nothing, and past the lease the SAME row is handed out again — which is what
// makes a crash between claim and submit recoverable (at-least-once).
func TestClaimDueRetriesLease(t *testing.T) {
	s := openTest(t)
	due := func(id string) {
		assert.NoErr(t, s.InsertRetry(RetryRecord{
			ID: id, SourceJobID: "job-1", Attempt: 2,
			RequestJSON: `{"project_key":"self"}`, Reason: "exit_code=7",
			NextRunAt: 100, CreatedAt: 1,
		}))
	}
	due("rt-aaaaaaa1")
	due("rt-aaaaaaa2")
	futureID := "rt-aaaaaaa3"
	assert.NoErr(t, s.InsertRetry(RetryRecord{
		ID: futureID, SourceJobID: "job-1", Attempt: 2,
		RequestJSON: `{"project_key":"self"}`, Reason: "exit_code=7",
		NextRunAt: 5000, CreatedAt: 1,
	}))

	// limit caps the batch: one of the two due rows, leased to now+lease.
	first, err := s.ClaimDueRetries(200, 1, 60)
	assert.NoErr(t, err)
	assert.Len(t, first, 1)
	assert.Eq(t, RetryClaimed, first[0].State)
	assert.Eq(t, int64(260), first[0].LeaseUntil)

	second, err := s.ClaimDueRetries(200, 10, 60)
	assert.NoErr(t, err)
	assert.Len(t, second, 1) // the other due row; the leased one stays out

	// Same instant, same clock: nothing is due (both rows are leased out).
	again, err := s.ClaimDueRetries(200, 10, 60)
	assert.NoErr(t, err)
	assert.Len(t, again, 0)

	// Lease lapsed (260 <= 261): both rows are claimable again, with a fresh lease.
	lapsed, err := s.ClaimDueRetries(261, 10, 60)
	assert.NoErr(t, err)
	assert.Len(t, lapsed, 2)
	for _, c := range lapsed {
		assert.Eq(t, RetryClaimed, c.State)
		assert.Eq(t, int64(321), c.LeaseUntil)
	}

	// A non-positive limit claims nothing; the future row joins once it is due.
	none, err := s.ClaimDueRetries(6000, 0, 60)
	assert.NoErr(t, err)
	assert.Len(t, none, 0)

	final, err := s.ClaimDueRetries(6000, 10, 60)
	assert.NoErr(t, err)
	found := false
	for _, c := range final {
		if c.ID == futureID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the future row must be claimable once due, got %+v", final)
	}
}

// TestMarkRetryDoneAndCancel: done and cancelled are terminal for a retry row —
// a done row can no longer be cancelled, and re-cancelling reports the error
// (the fixed choice: a rejected transition returns an error).
func TestMarkRetryDoneAndCancel(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertRetry(RetryRecord{
		ID: "rt-done0001", SourceJobID: "job-1", Attempt: 2,
		RequestJSON: `{}`, Reason: "exit_code=7", NextRunAt: 100, CreatedAt: 1,
	}))
	assert.NoErr(t, s.InsertRetry(RetryRecord{
		ID: "rt-cancel01", SourceJobID: "job-1", Attempt: 3,
		RequestJSON: `{}`, Reason: "exit_code=7", NextRunAt: 100, CreatedAt: 1,
	}))

	claimed, err := s.ClaimDueRetries(200, 10, 60)
	assert.NoErr(t, err)
	assert.Len(t, claimed, 2)

	assert.NoErr(t, s.MarkRetryDone("rt-done0001", "job-2"))
	rows, err := s.ListRetriesByJob("job-1")
	assert.NoErr(t, err)
	byID := map[string]RetryRecord{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if got := byID["rt-done0001"]; got.State != RetryDone || got.NewJobID != "job-2" || got.LeaseUntil != 0 {
		t.Fatalf("done row = %+v, want state=done new_job_id=job-2 lease_until=0", got)
	}
	if err := s.CancelRetry("rt-done0001"); err == nil {
		t.Fatal("cancelling a done retry must fail")
	}

	assert.NoErr(t, s.CancelRetry("rt-cancel01"))
	rows, _ = s.ListRetriesByJob("job-1")
	byID = map[string]RetryRecord{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if got := byID["rt-cancel01"]; got.State != RetryCancelled || got.LeaseUntil != 0 {
		t.Fatalf("cancelled row = %+v, want state=cancelled lease_until=0", got)
	}
	if err := s.CancelRetry("rt-cancel01"); err == nil {
		t.Fatal("re-cancelling a cancelled retry must fail")
	}

	// A cancelled row is never handed out again.
	due, err := s.ClaimDueRetries(100000, 10, 60)
	assert.NoErr(t, err)
	assert.Len(t, due, 0)
}

// TestPruneRemovesRetriesWithJob: a retry row is owned by its source job (the
// row exists to re-run it), so pruning the job cascades; another job's rows stay.
func TestPruneRemovesRetriesWithJob(t *testing.T) {
	s := openTest(t)
	now := int64(1_000_000)
	assert.NoErr(t, s.UpsertJob(termJob("old", "failed", now-100000, now-10*24*3600)))
	assert.NoErr(t, s.UpsertJob(termJob("recent", "failed", now-50000, now-3600)))
	assert.NoErr(t, s.InsertRetry(RetryRecord{
		ID: "rt-old00001", SourceJobID: "old", Attempt: 2,
		RequestJSON: `{}`, Reason: "exit_code=7", NextRunAt: now, CreatedAt: now,
	}))
	assert.NoErr(t, s.InsertRetry(RetryRecord{
		ID: "rt-new00001", SourceJobID: "recent", Attempt: 2,
		RequestJSON: `{}`, Reason: "exit_code=7", NextRunAt: now, CreatedAt: now,
	}))

	deleted, _, err := s.PruneJobs(RetentionPolicy{MaxAge: 7 * 24 * time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)

	gone, err := s.ListRetriesByJob("old")
	assert.NoErr(t, err)
	assert.Len(t, gone, 0)
	kept, err := s.ListRetriesByJob("recent")
	assert.NoErr(t, err)
	assert.Len(t, kept, 1)
}
