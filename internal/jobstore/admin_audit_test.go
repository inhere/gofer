package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestAdminAuditInsertListFilter(t *testing.T) {
	s := openTest(t)
	assert.NoError(t, s.InsertAdminAuditBatch(nil)) // an empty batch is a no-op
	assert.NoError(t, s.InsertAdminAuditBatch([]AdminAuditEntry{
		{At: 100, CallerID: "alice", Method: "POST", Route: "POST /v1/jobs", Status: 201},
		{At: 200, CallerID: "bob", Method: "DELETE", Route: "DELETE /v1/schedules/{id}", Target: "id=s1", Status: 403, Error: "permission required: x"},
		{At: 300, CallerID: "alice", Method: "POST", Route: "POST /v1/schedules/{id}/run", Target: "id=s2", QueryKeys: "a,b", Status: 500},
	}))
	all, next, err := s.ListAdminAudit(AdminAuditFilter{})
	assert.NoError(t, err)
	assert.Len(t, all, 3)
	assert.Eq(t, int64(0), next)
	assert.Eq(t, int64(300), all[0].At) // newest first
	assert.Eq(t, "a,b", all[0].QueryKeys)

	got, _, err := s.ListAdminAudit(AdminAuditFilter{Caller: "alice"})
	assert.NoError(t, err)
	assert.Len(t, got, 2)
	got, _, err = s.ListAdminAudit(AdminAuditFilter{Route: "/v1/schedules"})
	assert.NoError(t, err)
	assert.Len(t, got, 2)
	got, _, err = s.ListAdminAudit(AdminAuditFilter{Status: "ok"})
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	got, _, err = s.ListAdminAudit(AdminAuditFilter{Status: "fail"})
	assert.NoError(t, err)
	assert.Len(t, got, 2)
	got, _, err = s.ListAdminAudit(AdminAuditFilter{Since: 150, Until: 250})
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Eq(t, "bob", got[0].CallerID)
	_, _, err = s.ListAdminAudit(AdminAuditFilter{Status: "maybe"})
	assert.Err(t, err)
}

func TestAdminAuditCursorPaging(t *testing.T) {
	s := openTest(t)
	var batch []AdminAuditEntry
	for i := 1; i <= 5; i++ {
		batch = append(batch, AdminAuditEntry{At: int64(i), CallerID: "c", Method: "POST", Route: "POST /x", Status: 200})
	}
	assert.NoError(t, s.InsertAdminAuditBatch(batch))
	page1, next, err := s.ListAdminAudit(AdminAuditFilter{Limit: 2})
	assert.NoError(t, err)
	assert.Len(t, page1, 2)
	assert.NotEq(t, int64(0), next)
	page2, next2, err := s.ListAdminAudit(AdminAuditFilter{Limit: 2, Cursor: next})
	assert.NoError(t, err)
	assert.Len(t, page2, 2)
	assert.Lt(t, page2[0].ID, page1[1].ID)
	page3, next3, err := s.ListAdminAudit(AdminAuditFilter{Limit: 2, Cursor: next2})
	assert.NoError(t, err)
	assert.Len(t, page3, 1)
	assert.Eq(t, int64(0), next3)
}

// TestPruneAuditBeforeCoversBothTables: the retention sweep deletes old rows of admin_audit
// AND auth_audit, and keeps the newer ones.
func TestPruneAuditBeforeCoversBothTables(t *testing.T) {
	s := openTest(t)
	assert.NoError(t, s.InsertAdminAuditBatch([]AdminAuditEntry{
		{At: 10, Method: "POST", Route: "POST /old", Status: 200},
		{At: 1000, Method: "POST", Route: "POST /new", Status: 200},
	}))
	assert.NoError(t, s.InsertAuthAudit(AuthAuditEntry{At: 10, Action: "old"}))
	assert.NoError(t, s.InsertAuthAudit(AuthAuditEntry{At: 1000, Action: "new"}))
	admin, auth, err := s.PruneAuditBefore(500)
	assert.NoError(t, err)
	assert.Eq(t, int64(1), admin)
	assert.Eq(t, int64(1), auth)
	left, _, err := s.ListAdminAudit(AdminAuditFilter{})
	assert.NoError(t, err)
	assert.Len(t, left, 1)
	assert.Eq(t, "POST /new", left[0].Route)
	la, err := s.ListAuthAudit(AuthAuditFilter{})
	assert.NoError(t, err)
	assert.Len(t, la, 1)
	assert.Eq(t, "new", la[0].Action)
}
