package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func mustCapture(t *testing.T, s *Store, text string) WorkItem {
	t.Helper()
	w, err := s.CaptureWorkItem(CaptureInput{Text: text, Kind: WorkKindIdea, SourceSession: "sess-x", SourceJob: "job-x", By: "human:a"})
	assert.NoErr(t, err)
	return w
}

func TestWorkStatusesExcludeCaptured(t *testing.T) {
	for _, st := range WorkStatuses {
		assert.Neq(t, WorkCaptured, st)
	}
	assert.False(t, ValidWorkStatus(WorkCaptured))
	assert.True(t, ValidWorkStatusOrCaptured(WorkCaptured))
	assert.True(t, ValidWorkStatusOrCaptured(WorkParked))
	assert.False(t, ValidWorkStatusOrCaptured("bogus"))
}

func TestCaptureWorkItemInsert(t *testing.T) {
	s := openTest(t)
	w := mustCapture(t, s, "  记一下  ")
	assert.Eq(t, "记一下", w.Title)
	assert.Eq(t, WorkCaptured, w.Status)
	assert.Eq(t, WorkSourceHuman, w.StatusSource)
	assert.Eq(t, WorkKindIdea, w.Kind)
	assert.Eq(t, "sess-x", w.CaptureSourceSession)
	assert.Eq(t, "job-x", w.CaptureSourceJob)
	// The complete capture body is independent from the short title.
	long := strings.Repeat("想法", 1000)
	full := mustCapture(t, s, long)
	assert.Eq(t, long, full.CaptureText)
	assert.True(t, len([]rune(full.Title)) <= 121)
	assert.True(t, strings.HasPrefix(full.Title, "想法想法"))
	_, err := s.CaptureWorkItem(CaptureInput{Text: strings.Repeat("x", maxCaptureText+1), Kind: WorkKindNote})
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	// The source session is a field only: nothing is attached.
	ss, err := s.ListWorkItemSessions(w.ID)
	assert.NoErr(t, err)
	assert.Len(t, ss, 0)

	_, err = s.CaptureWorkItem(CaptureInput{Text: "x", Kind: "bogus"})
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, err = s.CaptureWorkItem(CaptureInput{Text: " ", Kind: WorkKindNote})
	assert.True(t, errors.Is(err, ErrWorkInvalid))
}

func TestCaptureWorkItemPreservesLongText(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("想", 500),
		strings.Repeat("想", 2000),
		strings.Repeat("😀", 2000),
		"第一行标题\n\n" + strings.Repeat("中文正文\n", 99) + "中文正文",
	} {
		t.Run(fmt.Sprint(len([]rune(text))), func(t *testing.T) {
			s := openTest(t)
			w := mustCapture(t, s, text)
			assert.Eq(t, text, w.CaptureText)
			assert.Eq(t, "", w.Goal)
			got, ok, err := s.GetWorkItem(w.ID)
			assert.NoErr(t, err)
			assert.True(t, ok)
			assert.Eq(t, text, got.CaptureText)
			if strings.Contains(text, "\n") {
				assert.Eq(t, "第一行标题", got.Title)
				matched, err := s.ListWorkItems(WorkListOpts{Statuses: []string{WorkCaptured}, Query: "中文正文"})
				assert.NoErr(t, err)
				assert.Len(t, matched, 1)
			}
		})
	}
}

func TestCaptureWorkItemRejectsOversizedTextWithoutWriting(t *testing.T) {
	s := openTest(t)
	_, err := s.CaptureWorkItem(CaptureInput{Text: strings.Repeat("想", 2001), Kind: WorkKindIdea})
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	items, err := s.ListWorkItems(WorkListOpts{Statuses: []string{WorkCaptured}})
	assert.NoErr(t, err)
	assert.Len(t, items, 0)
}

func TestListWorkItemsCaptured(t *testing.T) {
	s := openTest(t)
	c := mustCapture(t, s, "c1")
	a, err := s.CreateWorkItem(WorkItemInput{Title: "normal"})
	assert.NoErr(t, err)

	def, err := s.ListWorkItems(WorkListOpts{})
	assert.NoErr(t, err)
	assert.Len(t, def, 1)
	assert.Eq(t, a.ID, def[0].ID)
	all, err := s.ListWorkItems(WorkListOpts{IncludeClosed: true})
	assert.NoErr(t, err)
	assert.Len(t, all, 1)

	got, err := s.ListWorkItems(WorkListOpts{Statuses: []string{WorkCaptured}})
	assert.NoErr(t, err)
	assert.Len(t, got, 1)
	assert.Eq(t, c.ID, got[0].ID)

	_, err = s.ListWorkItems(WorkListOpts{Statuses: []string{"bogus"}})
	assert.True(t, errors.Is(err, ErrWorkInvalid))

	// Due listing skips captured even with a passed reminder.
	past := int64(1)
	_, _, err = s.UpdateWorkItem(c.ID, WorkItemPatch{RemindAt: &past}, 0, "human:a")
	assert.NoErr(t, err)
	due, err := s.ListWorkItems(WorkListOpts{Due: true, Now: 1000})
	assert.NoErr(t, err)
	assert.Len(t, due, 0)
}

func TestUpdateWorkItemCapturedRules(t *testing.T) {
	s := openTest(t)
	a, err := s.CreateWorkItem(WorkItemInput{Title: "normal"})
	assert.NoErr(t, err)
	capt := WorkCaptured
	// An ordinary update cannot move an item into captured.
	_, _, err = s.UpdateWorkItem(a.ID, WorkItemPatch{Status: &capt}, 0, "human:a")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	got, _, _ := s.GetWorkItem(a.ID)
	assert.Eq(t, WorkActive, got.Status)

	// ... nor out of it.
	c := mustCapture(t, s, "c1")
	parked := WorkParked
	_, _, err = s.UpdateWorkItem(c.ID, WorkItemPatch{Status: &parked}, 0, "human:a")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	got, _, _ = s.GetWorkItem(c.ID)
	assert.Eq(t, WorkCaptured, got.Status)

	// A field-only edit of a captured item keeps it captured.
	title := "c1 edited"
	got, _, err = s.UpdateWorkItem(c.ID, WorkItemPatch{Title: &title}, 0, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, WorkCaptured, got.Status)

	// The capture-box entry point may do both.
	got, _, err = s.UpdateWorkItem(c.ID, WorkItemPatch{Status: &parked, FromCapture: true}, 0, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, WorkParked, got.Status)
	got, _, err = s.UpdateWorkItem(a.ID, WorkItemPatch{Status: &capt, FromCapture: true}, 0, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, WorkCaptured, got.Status)
}

func TestMergeSplitRefuseCaptured(t *testing.T) {
	s := openTest(t)
	a, _ := s.CreateWorkItem(WorkItemInput{Title: "a"})
	b, _ := s.CreateWorkItem(WorkItemInput{Title: "b"})
	c := mustCapture(t, s, "c1")

	_, err := s.MergeWorkItems(a.ID, []string{c.ID}, "human:a")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, err = s.MergeWorkItems(c.ID, []string{a.ID}, "human:a")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, err = s.MergeWorkItems(a.ID, []string{b.ID}, "human:a")
	assert.NoErr(t, err)

	_, _, err = s.SplitWorkItem(c.ID, SplitWorkItemInput{Title: "part"})
	assert.True(t, errors.Is(err, ErrWorkInvalid))
}

func TestWorkCountsAndDueExcludeCaptured(t *testing.T) {
	s := openTest(t)
	c := mustCapture(t, s, "c1")
	_, err := s.CreateWorkItem(WorkItemInput{Title: "normal"})
	assert.NoErr(t, err)
	counts, err := s.WorkCounts()
	assert.NoErr(t, err)
	_, has := counts[WorkCaptured]
	assert.False(t, has)
	assert.Eq(t, 1, counts[WorkActive])

	past := int64(5)
	_, _, err = s.UpdateWorkItem(c.ID, WorkItemPatch{RemindAt: &past}, 0, "human:a")
	assert.NoErr(t, err)
	due, err := s.ListDueWorkItems(1000)
	assert.NoErr(t, err)
	assert.Len(t, due, 0)
}

func TestSetWorkItemAutoStatusSkipsCaptured(t *testing.T) {
	s := openTest(t)
	c := mustCapture(t, s, "c1")
	changed, err := s.SetWorkItemAutoStatus(c.ID, WorkReview, "x")
	assert.NoErr(t, err)
	assert.False(t, changed)
	_, err = s.SetWorkItemAutoStatus(c.ID, WorkCaptured, "x")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
}

func TestMigrateWorkItemsCaptureColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE work_items (
  id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', goal TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active', status_source TEXT NOT NULL DEFAULT 'auto',
  blocker_kind TEXT NOT NULL DEFAULT '', blocker_text TEXT NOT NULL DEFAULT '', next_step TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '', project_key TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL DEFAULT 0, park_until INTEGER NOT NULL DEFAULT 0, park_note TEXT NOT NULL DEFAULT '',
  remind_at INTEGER NOT NULL DEFAULT 0, reminded_at INTEGER NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT 'human',
  unsorted INTEGER NOT NULL DEFAULT 0, merged_into TEXT NOT NULL DEFAULT '', rev INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL DEFAULT '',
  closed_at INTEGER NOT NULL DEFAULT 0, last_activity_at INTEGER NOT NULL DEFAULT 0, status_at INTEGER NOT NULL DEFAULT 0)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO work_items(id, title, created_at, updated_at) VALUES ('w-old','旧条目',1,1)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()
	for _, col := range []string{"capture_text", "kind", "capture_source_session", "capture_source_job"} {
		assert.True(t, tableHasColumn(t, s, "work_items", col), "work_items.%s", col)
	}
	w, ok, err := s.GetWorkItem("w-old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "旧条目", w.Title)
	assert.Eq(t, "", w.CaptureText)
	assert.Eq(t, "", w.Kind)
	// Re-opening is idempotent.
	assert.NoErr(t, s.migrate())
	assert.NoErr(t, s.migrateWorkItems())
}
