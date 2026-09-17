package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// TestJobReviewFieldsRoundTrip: the人工验收 (GATE-01 S3) audit fields are persisted
// job properties — a reviewed job still answers "who accepted/rejected it, when and
// why" after the process that ran it is gone, and a job that never asked for review
// reads back the zero values.
func TestJobReviewFieldsRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleJob("rv-1", "alpha", 2000)
	in.Status = "rejected"
	in.RequireReview = true
	in.ReviewedBy = "alice"
	in.ReviewedAt = 1_700_000_123
	in.ReviewNote = "not acceptable"
	assert.NoErr(t, s.UpsertJob(in))

	got, ok, err := s.GetJob("rv-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, got.RequireReview)
	assert.Eq(t, "alice", got.ReviewedBy)
	assert.Eq(t, int64(1_700_000_123), got.ReviewedAt)
	assert.Eq(t, "not acceptable", got.ReviewNote)

	// The review columns are updated (not only inserted) by the upsert.
	in.ReviewedBy = "bob"
	in.ReviewNote = "second look"
	assert.NoErr(t, s.UpsertJob(in))
	got, _, err = s.GetJob("rv-1")
	assert.NoErr(t, err)
	assert.Eq(t, "bob", got.ReviewedBy)
	assert.Eq(t, "second look", got.ReviewNote)

	plain := sampleJob("rv-plain", "alpha", 2001)
	assert.NoErr(t, s.UpsertJob(plain))
	got2, _, err := s.GetJob("rv-plain")
	assert.NoErr(t, err)
	assert.False(t, got2.RequireReview)
	assert.Eq(t, "", got2.ReviewedBy)
	assert.Eq(t, int64(0), got2.ReviewedAt)
	assert.Eq(t, "", got2.ReviewNote)
}

// TestPruneSkipsNeedsReviewKeepsRejected: `rejected` is a terminal state, so
// retention collects it; `needs_review` is NOT, so a job still waiting for a human
// is never evicted out from under the reviewer.
func TestPruneSkipsNeedsReviewKeepsRejected(t *testing.T) {
	s := openTest(t)
	now := int64(3_000_000)
	old := now - 10*24*3600

	assert.NoErr(t, s.UpsertJob(termJob("reviewing", "needs_review", old, old)))
	assert.NoErr(t, s.UpsertJob(termJob("rejected", "rejected", old, old)))

	deleted, dirs, err := s.PruneJobs(RetentionPolicy{MaxAge: 7 * 24 * time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)
	assert.Eq(t, []string{"/tmp/results/rejected"}, dirs)
	assert.Eq(t, []string{"reviewing"}, remainingIDs(t, s))
}
