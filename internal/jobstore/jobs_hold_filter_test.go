package jobstore

import (
	"sort"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// holdFilterRow upserts one row with the given status and raw hold_json.
func holdFilterRow(t *testing.T, s *Store, id, status, holdJSON string, startedAt int64) {
	t.Helper()
	rec := sampleJob(id, "proj", startedAt)
	rec.Status = status
	rec.HoldJSON = holdJSON
	assert.NoErr(t, s.UpsertJob(rec))
}

func holdFilterIDs(t *testing.T, s *Store, hold string) []string {
	t.Helper()
	rows, err := s.ListJobs(ListQuery{Hold: hold})
	assert.NoErr(t, err)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestValidHoldFilter(t *testing.T) {
	for _, v := range []string{"pending", "decided", "any"} {
		assert.True(t, ValidHoldFilter(v), v)
	}
	for _, v := range []string{"", "bad", "Pending", " any"} {
		assert.False(t, ValidHoldFilter(v), v)
	}
}

func TestListJobsHoldFilter(t *testing.T) {
	s := openTest(t)
	holdFilterRow(t, s, "pending", statusAwaitingApproval, `{"reason":"push","expires_at":9}`, 100)
	holdFilterRow(t, s, "approved", "running", `{"decision":"approved","decided_by":"a"}`, 101)
	holdFilterRow(t, s, "rejected", "cancelled", `{"decision":"rejected"}`, 102)
	holdFilterRow(t, s, "expired", "cancelled", `{"decision":"expired","decided_by":"system"}`, 103)
	holdFilterRow(t, s, "withdrawn", "cancelled", `{"decision":"withdrawn"}`, 104)
	// An update park carries kind=update: never a human decision, even while awaiting.
	holdFilterRow(t, s, "updatepark", statusAwaitingApproval, `{"kind":"update","reason":"upd"}`, 105)
	holdFilterRow(t, s, "updatedone", "queued", `{"kind":"update","decision":"approved"}`, 106)
	// Rows that must not error the query: malformed JSON, empty string, no hold at all.
	holdFilterRow(t, s, "badjson", statusAwaitingApproval, `{not json`, 107)
	holdFilterRow(t, s, "emptyjson", statusAwaitingApproval, ``, 108)
	holdFilterRow(t, s, "plain", "done", ``, 109)

	assert.Eq(t, []string{"pending"}, holdFilterIDs(t, s, HoldFilterPending))
	assert.Eq(t, []string{"approved", "expired", "rejected", "withdrawn"}, holdFilterIDs(t, s, HoldFilterDecided))
	assert.Eq(t, []string{"approved", "expired", "pending", "rejected", "withdrawn"}, holdFilterIDs(t, s, HoldFilterAny))
	assert.Eq(t, 10, len(holdFilterIDs(t, s, "")))

	_, err := s.ListJobs(ListQuery{Hold: "bad"})
	assert.Err(t, err)
}
