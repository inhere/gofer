package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestStoredCallerExpiryRoundTripAndTouch: expires_at survives insert / update, last_used_at is
// written only by TouchCallersUsed (never lowered, never clobbered by a caller edit), an
// unknown id is skipped.
func TestStoredCallerExpiryRoundTripAndTouch(t *testing.T) {
	s := openTest(t)
	assert.NoError(t, s.WithCallerTx(func(tx CallerTx) error {
		return tx.InsertCallerTx(StoredCaller{ID: "ci", TokenHash: "h", ExpiresAt: 5000})
	}))
	row, ok, err := s.GetStoredCaller("ci")
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(5000), row.ExpiresAt)
	assert.Eq(t, int64(0), row.LastUsedAt)

	assert.NoError(t, s.TouchCallersUsed(map[string]int64{"ci": 300, "ghost": 9}))
	assert.NoError(t, s.TouchCallersUsed(map[string]int64{"ci": 200})) // older: ignored
	assert.NoError(t, s.TouchCallersUsed(nil))
	row, _, _ = s.GetStoredCaller("ci")
	assert.Eq(t, int64(300), row.LastUsedAt)

	// An edit that read the row BEFORE the touch must not lose the stamp, and can clear the expiry.
	stale := row
	stale.LastUsedAt = 0
	stale.ExpiresAt = 0
	assert.NoError(t, s.WithCallerTx(func(tx CallerTx) error { return tx.UpdateCallerTx(stale) }))
	row, _, _ = s.GetStoredCaller("ci")
	assert.Eq(t, int64(300), row.LastUsedAt)
	assert.Eq(t, int64(0), row.ExpiresAt)
}
