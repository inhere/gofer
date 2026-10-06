package jobstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestStewardJobMarker(t *testing.T) {
	s := openTest(t)
	assert.False(t, s.IsStewardJob("j1"))
	assert.NoErr(t, s.MarkStewardJob("j1"))
	assert.NoErr(t, s.MarkStewardJob("j1")) // idempotent
	assert.True(t, s.IsStewardJob("j1"))
	assert.False(t, s.IsStewardJob("j2"))
	assert.True(t, ValidJobCredentialKind(JobCredentialSteward))
}

func TestStewardEventsDedupeAndHandled(t *testing.T) {
	s := openTest(t)
	added, err := s.AddStewardEvent(StewardEventSession, "s1:offline", "会话离线")
	assert.NoErr(t, err)
	assert.True(t, added)
	added, err = s.AddStewardEvent(StewardEventSession, "s1:offline", "again")
	assert.NoErr(t, err)
	assert.False(t, added)
	_, _ = s.AddStewardEvent(StewardEventDue, "w1:100", "到期")
	ev, err := s.PendingStewardEvents(10)
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(ev))
	assert.NoErr(t, s.MarkStewardEventsHandled([]int64{ev[0].ID}, 500))
	ev, _ = s.PendingStewardEvents(10)
	assert.Eq(t, 1, len(ev))
	assert.Eq(t, StewardEventDue, ev[0].Kind)
	// A handled occurrence is not re-noted either.
	added, _ = s.AddStewardEvent(StewardEventSession, "s1:offline", "")
	assert.False(t, added)
}

func TestStewardReviewLifecycleAndComment(t *testing.T) {
	s := openTest(t)
	_, err := s.SetStewardReviewSummary("2026-10-06", "没有 review")
	assert.True(t, errors.Is(err, ErrStewardNoReview))

	id, err := s.BeginStewardReview("2026-10-06", "daily", []string{"w1", "w2"})
	assert.NoErr(t, err)
	r, err := s.SetStewardReviewSummary("2026-10-06", "三件事都在等同一批设备")
	assert.NoErr(t, err)
	assert.Eq(t, id, r.ID)
	// While running there is no digest comment yet.
	c, _ := s.StewardReviewComment("2026-10-06")
	assert.Eq(t, "", c)
	assert.NoErr(t, s.FinishStewardReview(id, StewardReviewDone, "", "job1", ""))
	c, _ = s.StewardReviewComment("2026-10-06")
	assert.Eq(t, "三件事都在等同一批设备", c)
	last, ok, _ := s.LastStewardReview()
	assert.True(t, ok)
	assert.Eq(t, "job1", last.JobID)
	assert.Eq(t, "w1,w2", last.ItemIDs)
	assert.True(t, last.EndedAt > 0)

	// A skipped review is not "the last review that ran".
	sid, _ := s.BeginStewardReview("2026-10-07", "daily", nil)
	assert.NoErr(t, s.FinishStewardReview(sid, StewardReviewSkipped, "", "", ""))
	last, _, _ = s.LastStewardReview()
	assert.Eq(t, id, last.ID)

	// A summary after the run (nothing running) attaches to today's done review.
	r2, err := s.SetStewardReviewSummary("2026-10-06", "补充点评")
	assert.NoErr(t, err)
	assert.Eq(t, id, r2.ID)
	assert.NoErr(t, s.FailStaleStewardReviews(1<<40))
}

func TestWorkMergeSuggestionRecordsAndDedupes(t *testing.T) {
	s := openTest(t)
	a, _ := s.CreateWorkItem(WorkItemInput{Title: "登录修复"})
	b, _ := s.CreateWorkItem(WorkItemInput{Title: "登录问题"})
	sg, stored, err := s.AddWorkMergeSuggestion(a.ID, b.ID, "同一个工作区、内容相近", "steward(claude-acp)")
	assert.NoErr(t, err)
	assert.True(t, stored)
	assert.Eq(t, MergeSuggestPending, sg.State)
	again, stored, err := s.AddWorkMergeSuggestion(b.ID, a.ID, "反向也是同一对", "steward(claude-acp)")
	assert.NoErr(t, err)
	assert.False(t, stored)
	assert.Eq(t, sg.ID, again.ID)

	j, _ := s.ListWorkJournal(a.ID, 10, 0)
	found := false
	for _, e := range j {
		if e.Kind == WorkJournalSteward && strings.Contains(e.Text, "合并建议") {
			found = true
		}
	}
	assert.True(t, found)

	// Nothing merged by recording a suggestion.
	cur, _, _ := s.GetWorkItem(b.ID)
	assert.Eq(t, "", cur.MergedInto)

	_, _, err = s.AddWorkMergeSuggestion(a.ID, a.ID, "", "x")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, _, err = s.AddWorkMergeSuggestion(a.ID, "nope", "", "x")
	assert.True(t, errors.Is(err, ErrWorkItemNotFound))

	list, _ := s.ListWorkMergeSuggestions("")
	assert.Eq(t, 1, len(list))
	assert.NoErr(t, s.ResolveWorkMergeSuggestion(sg.ID, MergeSuggestDismissed))
	assert.True(t, errors.Is(s.ResolveWorkMergeSuggestion(sg.ID, MergeSuggestAccepted), ErrMergeSuggestionNotFound))
	list, _ = s.ListWorkMergeSuggestions("")
	assert.Eq(t, 0, len(list))
}

func TestListRecentWorkJournalWindow(t *testing.T) {
	s := openTest(t)
	w, _ := s.CreateWorkItem(WorkItemInput{Title: "x"})
	_, err := s.AppendWorkJournal(w.ID, WorkJournalNote, "一条备注", "human:a")
	assert.NoErr(t, err)
	got, err := s.ListRecentWorkJournal(0, 50)
	assert.NoErr(t, err)
	assert.True(t, len(got) >= 1)
	got, _ = s.ListRecentWorkJournal(1<<40, 50)
	assert.Eq(t, 0, len(got))
}
