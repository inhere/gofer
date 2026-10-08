package jobstore

import (
	"errors"
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

	// Audit: one work.deleted row, actor only, no title.
	var detail string
	assert.NoErr(t, s.db.QueryRow(`SELECT detail_json FROM job_events WHERE job_id=? AND type=?`, a.ID, WorkDeletedEvent).Scan(&detail))
	assert.True(t, strings.Contains(detail, "alice"))
	assert.False(t, strings.Contains(detail, "private title"))

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
