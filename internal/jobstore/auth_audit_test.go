package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestAuthAuditUsageAndRetention(t *testing.T) {
	s := openTest(t)
	grant, ok, err := s.GetAuthGrant("seed:users:job.submit")
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.NoError(t, s.MarkAuthGrantUsed(grant.ID, 200))
	assert.NoError(t, s.MarkAuthGrantUsed(grant.ID, 201))
	got, ok, err := s.GetAuthGrant(grant.ID)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(2), got.UseCount)
	assert.Eq(t, int64(201), got.LastUsedAt)

	assert.NoError(t, s.InsertAuthAudit(AuthAuditEntry{At: 1, Action: "old", GrantID: grant.ID}))
	assert.NoError(t, s.InsertAuthAudit(AuthAuditEntry{At: 90*24*60*60 + 1, Action: "new", GrantID: grant.ID}))
	assert.NoError(t, s.PruneAuthAudit(90*24*60*60+time.Hour.Milliseconds()/1000))
	entries, err := s.ListAuthAudit(AuthAuditFilter{GrantID: grant.ID})
	assert.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Eq(t, "new", entries[0].Action)
}
