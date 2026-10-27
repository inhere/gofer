package jobstore

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestMigrateCapturedDispatchFinalizesOnlyLinkedDispatches(t *testing.T) {
	s := openTest(t)
	moved, err := s.CreateWorkItem(WorkItemInput{Title: "old issue", Status: WorkParked, By: "human:a"})
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(moved.ID, WorkLinkIssue, "ISS-1", "human:a")
	assert.NoErr(t, err)
	_, err = s.AppendWorkJournal(moved.ID, WorkJournalNote, "已转为 issue ISS-1", "human:a")
	assert.NoErr(t, err)
	ordinary, err := s.CreateWorkItem(WorkItemInput{Title: "ordinary parked", Status: WorkParked, By: "human:a"})
	assert.NoErr(t, err)
	_, err = s.db.Exec(`DELETE FROM work_kv WHERE k=?`, "migrated_captured_dispatch_finalizes")
	assert.NoErr(t, err)
	assert.NoErr(t, s.migrateCapturedDispatchFinalizes())
	got, _, err := s.GetWorkItem(moved.ID)
	assert.NoErr(t, err)
	assert.Eq(t, WorkDone, got.Status)
	got, _, err = s.GetWorkItem(ordinary.ID)
	assert.NoErr(t, err)
	assert.Eq(t, WorkParked, got.Status)
	assert.NoErr(t, s.migrateCapturedDispatchFinalizes())
	journal, err := s.ListWorkJournal(moved.ID, 20, 0)
	assert.NoErr(t, err)
	assert.True(t, strings.Contains(journal[len(journal)-1].Text, "已转为 issue ISS-1"))
}
