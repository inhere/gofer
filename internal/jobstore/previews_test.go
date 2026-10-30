package jobstore

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestPreviewTokenHashIsStable(t *testing.T) {
	h := PreviewTokenHash("tok-a")
	assert.Eq(t, h, PreviewTokenHash("tok-a"))
	assert.NotEq(t, h, PreviewTokenHash("tok-b"))
	assert.Eq(t, 64, len(h))
	assert.False(t, strings.Contains(h, "tok-a"))
}

// The plaintext token is never stored: the row holds the hash, and the lease is found
// again by hashing the presented token.
func TestPreviewRoundTripStoresOnlyTokenHash(t *testing.T) {
	s := openTest(t)
	const token = "secret-preview-token"
	rec := PreviewRecord{
		ID: "pv-1", TokenHash: PreviewTokenHash(token), JobID: "job-1", CallerID: "caller-a",
		WorkerID: "local", WorkerInstanceID: "inst-1", Port: 18100, State: "active",
		CreatedAt: 100, ExpiresAt: 200,
	}
	assert.NoErr(t, s.InsertPreview(rec))

	got, ok, err := s.GetPreview("pv-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, rec, got)
	assert.NotEq(t, token, got.TokenHash)

	byHash, ok, err := s.GetPreviewByTokenHash(PreviewTokenHash(token))
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "pv-1", byHash.ID)
	_, ok, err = s.GetPreviewByTokenHash(token)
	assert.NoErr(t, err)
	assert.False(t, ok, "the plaintext token must not match a row")

	// Nothing in the table carries the plaintext.
	var n int
	assert.NoErr(t, s.db.QueryRow(`SELECT COUNT(*) FROM job_previews WHERE id LIKE ? OR token_hash LIKE ? OR job_id LIKE ? OR caller_id LIKE ? OR worker_id LIKE ? OR worker_instance_id LIKE ? OR IFNULL(close_reason,'') LIKE ?`,
		"%"+token+"%", "%"+token+"%", "%"+token+"%", "%"+token+"%", "%"+token+"%", "%"+token+"%", "%"+token+"%").Scan(&n))
	assert.Eq(t, 0, n)

	// A second lease with the same token hash is refused (UNIQUE).
	dup := rec
	dup.ID = "pv-2"
	assert.Err(t, s.InsertPreview(dup))
}

func TestPreviewStateUpdatesAndList(t *testing.T) {
	s := openTest(t)
	for i, id := range []string{"pv-a", "pv-b", "pv-c"} {
		assert.NoErr(t, s.InsertPreview(PreviewRecord{
			ID: id, TokenHash: PreviewTokenHash(id), JobID: "job-1", CallerID: "c", WorkerID: "local",
			WorkerInstanceID: "local", Port: 18100 + i, State: "active", CreatedAt: int64(10 + i), ExpiresAt: int64(100 + i*100),
		}))
	}
	assert.NoErr(t, s.InsertPreview(PreviewRecord{
		ID: "pv-x", TokenHash: PreviewTokenHash("x"), JobID: "job-2", CallerID: "c", WorkerID: "local",
		WorkerInstanceID: "local", Port: 18200, State: "active", CreatedAt: 1, ExpiresAt: 1000,
	}))

	list, err := s.ListPreviews("job-1")
	assert.NoErr(t, err)
	assert.Eq(t, 3, len(list))
	assert.Eq(t, "pv-c", list[0].ID, "newest first")

	assert.NoErr(t, s.UpdatePreviewState("pv-a", "closed", "revoked", 50))
	got, _, _ := s.GetPreview("pv-a")
	assert.Eq(t, "closed", got.State)
	assert.Eq(t, "revoked", got.CloseReason)
	assert.Eq(t, int64(50), got.ClosedAt)

	// pv-b expires at 200, pv-c at 300.
	assert.NoErr(t, s.ExpirePreviews(250))
	b, _, _ := s.GetPreview("pv-b")
	c, _, _ := s.GetPreview("pv-c")
	assert.Eq(t, "expired", b.State)
	assert.Eq(t, "active", c.State)

	assert.NoErr(t, s.ClosePreviewsForJob("job-1", "job_ended", 400))
	c, _, _ = s.GetPreview("pv-c")
	assert.Eq(t, "job_ended", c.State)
	x, _, _ := s.GetPreview("pv-x")
	assert.Eq(t, "active", x.State, "another job's lease is untouched")

	assert.NoErr(t, s.TouchPreview("pv-x", 3))
	x, _, _ = s.GetPreview("pv-x")
	assert.Eq(t, 3, x.ConnectionCount)
	assert.True(t, x.LastUsedAt > 0)

	_, ok, err := s.GetPreview("missing")
	assert.NoErr(t, err)
	assert.False(t, ok)
}
