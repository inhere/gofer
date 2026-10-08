package jobstore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func workRowCount(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	assert.NoErr(t, s.db.QueryRow(q, args...).Scan(&n))
	return n
}

func TestDeleteWorkItemRemovesEverythingAndAudits(t *testing.T) {
	s := openTest(t)
	mustSession(t, s, "sess-del")
	a, err := s.CreateWorkItem(WorkItemInput{Title: "private title", ProjectKey: "p1", SessionIDs: []string{"sess-del"}, By: "human:a"})
	assert.NoErr(t, err)
	b, err := s.CreateWorkItem(WorkItemInput{Title: "merged source", By: "human:a"})
	assert.NoErr(t, err)
	c, err := s.CreateWorkItem(WorkItemInput{Title: "other", By: "human:a"})
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(a.ID, "issue", "i-1", "human:a")
	assert.NoErr(t, err)
	_, err = s.AppendWorkJournal(a.ID, WorkJournalNote, "note", "human:a")
	assert.NoErr(t, err)
	_, err = s.CreateWorkRequest(WorkRequestInput{WorkItemID: a.ID, Kind: WorkRequestReport, By: "human:a"})
	assert.NoErr(t, err)
	_, err = s.UpsertWorkSuggestion(WorkSuggestion{WorkItemID: a.ID, Field: "goal", Value: "g", By: "steward"})
	assert.NoErr(t, err)
	_, err = s.db.Exec(`INSERT INTO work_summaries(work_item_id, at, state) VALUES (?,1,'done')`, a.ID)
	assert.NoErr(t, err)
	_, _, err = s.AddWorkMergeSuggestion(a.ID, c.ID, "dup", "steward")
	assert.NoErr(t, err)
	_, err = s.AddStewardEvent(StewardEventDue, a.ID+"|due|1", "工作项 "+a.ID+"「private title」")
	assert.NoErr(t, err)
	_, err = s.MergeWorkItems(a.ID, []string{b.ID}, "human:a")
	assert.NoErr(t, err)

	// Still open: refused, nothing removed.
	err = s.DeleteWorkItem(a.ID, "alice")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	st := WorkDropped
	_, _, err = s.UpdateWorkItem(a.ID, WorkItemPatch{Status: &st}, 0, "human:a")
	assert.NoErr(t, err)

	assert.NoErr(t, s.DeleteWorkItem(a.ID, "alice"))

	for _, q := range []string{
		`SELECT COUNT(*) FROM work_items WHERE id=?`,
		`SELECT COUNT(*) FROM work_item_sessions WHERE work_item_id=?`,
		`SELECT COUNT(*) FROM work_links WHERE work_item_id=?`,
		`SELECT COUNT(*) FROM work_journal WHERE work_item_id=?`,
		`SELECT COUNT(*) FROM work_field_sources WHERE work_item_id=?`,
		`SELECT COUNT(*) FROM work_requests WHERE work_item_id=?`,
		`SELECT COUNT(*) FROM work_suggestions WHERE work_item_id=?`,
		`SELECT COUNT(*) FROM work_summaries WHERE work_item_id=?`,
		`SELECT COUNT(*) FROM work_merge_suggestions WHERE target_id=? OR source_id=?`,
		`SELECT COUNT(*) FROM steward_events WHERE ref LIKE ?||'%'`,
	} {
		args := []any{a.ID}
		if strings.Count(q, "?") == 2 {
			args = append(args, a.ID)
		}
		assert.Eq(t, 0, workRowCount(t, s, q, args...), q)
	}
	// The merged source survives with its pointer cleared; the unrelated item is untouched.
	got, ok, err := s.GetWorkItem(b.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", got.MergedInto)
	_, ok, _ = s.GetWorkItem(c.ID)
	assert.True(t, ok)

	// Audit: one work.deleted row in audit_events (actor only, no title) and none in job_events.
	evs, err := s.ListAuditEvents(WorkDeletedEvent, a.ID)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(evs))
	assert.Eq(t, "alice", evs[0].Actor)
	assert.Eq(t, a.ID, evs[0].TargetID)
	var detail string
	assert.NoErr(t, s.db.QueryRow(`SELECT detail_json FROM audit_events WHERE target_id=?`, a.ID).Scan(&detail))
	assert.False(t, strings.Contains(detail, "private title"))
	assert.Eq(t, 0, workRowCount(t, s, `SELECT COUNT(*) FROM job_events WHERE type=?`, WorkDeletedEvent))

	assert.True(t, errors.Is(s.DeleteWorkItem(a.ID, "alice"), ErrWorkItemNotFound))
}

func TestListFinalWorkItemIDs(t *testing.T) {
	s := openTest(t)
	a, _ := s.CreateWorkItem(WorkItemInput{Title: "a"})
	b, _ := s.CreateWorkItem(WorkItemInput{Title: "b"})
	dropped, done := WorkDropped, WorkDone
	_, _, err := s.UpdateWorkItem(a.ID, WorkItemPatch{Status: &dropped}, 0, "h")
	assert.NoErr(t, err)
	_, _, err = s.UpdateWorkItem(b.ID, WorkItemPatch{Status: &done}, 0, "h")
	assert.NoErr(t, err)
	ids, err := s.ListFinalWorkItemIDs(WorkDropped)
	assert.NoErr(t, err)
	assert.Eq(t, []string{a.ID}, ids)
	_, err = s.ListFinalWorkItemIDs(WorkActive)
	assert.True(t, errors.Is(err, ErrWorkInvalid))
}

// eb2k: v0.122 databases hold work.deleted rows in job_events; reopening moves them into
// audit_events exactly once and leaves unrelated events alone.
func TestMigrateWorkDeletedAuditFromJobEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	s, err := Open(path)
	assert.NoErr(t, err)
	for _, e := range []struct{ id, typ, detail string }{
		{"w-old1", WorkDeletedEvent, `{"actor":"bob"}`},
		{"w-old2", WorkDeletedEvent, `{"actor":"carol"}`},
		{"job-1", "job.created", `{}`},
	} {
		_, err := s.db.Exec(`INSERT INTO job_events (job_id,type,detail_json,at) VALUES (?,?,?,?)`, e.id, e.typ, e.detail, 1000)
		assert.NoErr(t, err)
	}
	// Simulate "never migrated": drop the marker Open already wrote.
	_, err = s.db.Exec(`DELETE FROM work_kv WHERE k='migrated_work_deleted_audit'`)
	assert.NoErr(t, err)
	assert.NoErr(t, s.Close())

	for i := 0; i < 2; i++ { // the second Open must be a no-op
		s, err = Open(path)
		assert.NoErr(t, err)
		evs, err := s.ListAuditEvents(WorkDeletedEvent, "")
		assert.NoErr(t, err)
		assert.Eq(t, 2, len(evs))
		assert.Eq(t, "w-old1", evs[0].TargetID)
		assert.Eq(t, "bob", evs[0].Actor)
		assert.Eq(t, int64(1000), evs[0].At)
		assert.Eq(t, "carol", evs[1].Actor)
		assert.Eq(t, 0, workRowCount(t, s, `SELECT COUNT(*) FROM job_events WHERE type=?`, WorkDeletedEvent))
		assert.Eq(t, 1, workRowCount(t, s, `SELECT COUNT(*) FROM job_events WHERE type='job.created'`))
		assert.NoErr(t, s.Close())
	}
}
