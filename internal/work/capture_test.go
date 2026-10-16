package work

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func mustCaptured(t *testing.T, st *jobstore.Store, text string) jobstore.WorkItem {
	t.Helper()
	w, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: text, Kind: jobstore.WorkKindIdea, By: "human:a"})
	assert.NoErr(t, err)
	return w
}

func statusOf(t *testing.T, st *jobstore.Store, id string) string {
	t.Helper()
	w, ok, err := st.GetWorkItem(id)
	assert.NoErr(t, err)
	assert.True(t, ok)
	return w.Status
}

func TestReportCannotTouchCapturedStatus(t *testing.T) {
	svc, st, _ := newSvc(t)
	c := mustCaptured(t, st, "随手记")

	// Asking for captured is invalid.
	_, err := svc.Report(c.ID, ReportInput{Status: jobstore.WorkCaptured})
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
	// A normal status in a report is noted but not adopted; other fields still land.
	for _, s := range []string{jobstore.WorkActive, jobstore.WorkParked, jobstore.WorkNeedsMe} {
		w, err := svc.Report(c.ID, ReportInput{Status: s, Summary: "进展 " + s})
		assert.NoErr(t, err)
		assert.Eq(t, jobstore.WorkCaptured, w.Status)
		assert.Eq(t, jobstore.WorkSourceHuman, w.StatusSource)
	}
	assert.Eq(t, jobstore.WorkCaptured, statusOf(t, st, c.ID))
}

func TestSummarizerStatusHintNeverTouchesCaptured(t *testing.T) {
	svc, st, _ := newSvc(t)
	c := mustCaptured(t, st, "随手记")

	// The summarizer's hint is not even stored for a captured item.
	_, _, hint := svc.applySummary(c.ID, SummaryOut{StatusHint: jobstore.WorkParked, Next: "x"}, "summarizer(claude)", "")
	assert.Eq(t, "", hint)
	sugg, _ := st.ListWorkSuggestions(c.ID)
	for _, s := range sugg {
		assert.NotEq(t, jobstore.SuggestStatusHint, s.Field)
	}
	// "captured" is not a valid hint for any item.
	n, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "normal"})
	assert.NoErr(t, err)
	_, _, hint = svc.applySummary(n.ID, SummaryOut{StatusHint: jobstore.WorkCaptured}, "summarizer(claude)", "")
	assert.Eq(t, "", hint)

	// A suggestion that already exists cannot be adopted into a captured item.
	_, err = st.UpsertWorkSuggestion(jobstore.WorkSuggestion{WorkItemID: c.ID, Field: jobstore.SuggestStatusHint, Value: jobstore.WorkParked, By: "summarizer"})
	assert.NoErr(t, err)
	_, err = svc.AcceptSuggestion(c.ID, jobstore.SuggestStatusHint, "human:a")
	assert.True(t, errors.Is(err, ErrSuggestionConflict))
	assert.Eq(t, jobstore.WorkCaptured, statusOf(t, st, c.ID))
}

func TestStewardUpdateCannotTouchCapturedStatus(t *testing.T) {
	svc, st, _ := newSvc(t)
	c := mustCaptured(t, st, "随手记")

	bad := jobstore.WorkCaptured
	_, err := svc.StewardUpdate(c.ID, jobstore.WorkItemPatch{Status: &bad}, 0, "steward(x)")
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))

	parked := jobstore.WorkParked
	next := "下一步"
	res, err := svc.StewardUpdate(c.ID, jobstore.WorkItemPatch{Status: &parked, NextStep: &next}, 0, "steward(x)")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkCaptured, res.Item.Status)
	assert.Eq(t, "下一步", res.Item.NextStep) // the rest of the patch still applies
	assert.Len(t, res.Notes, 1)
	assert.True(t, strings.Contains(res.Notes[0], "收集箱"))
}

func TestDigestExcludesCaptured(t *testing.T) {
	svc, st, _ := newSvc(t)
	loc := time.FixedZone("x", 8*3600)
	now := time.Date(2026, 10, 5, 9, 10, 0, 0, loc)
	svc.SetNow(func() time.Time { return now })
	st.SetClock(func() time.Time { return time.Date(2026, 10, 4, 15, 0, 0, 0, loc) }) // "yesterday"
	mustCaptured(t, st, "昨天随手记的想法")
	st.SetClock(func() time.Time { return now })

	d, err := svc.BuildDigest(now)
	assert.NoErr(t, err)
	assert.Eq(t, 0, d.Yesterday)
	assert.False(t, strings.Contains(d.Text, "昨天随手记的想法"))
}

func TestRemindersHealthAndSummaryExcludeCaptured(t *testing.T) {
	svc, st, n := newSvc(t)
	now := time.Unix(1_800_000_000, 0)
	svc.SetNow(func() time.Time { return now })
	c := mustCaptured(t, st, "有提醒的想法")
	past := now.Unix() - 5
	_, _, err := st.UpdateWorkItem(c.ID, jobstore.WorkItemPatch{RemindAt: &past}, 0, "human:a")
	assert.NoErr(t, err)

	svc.Tick(now)
	assert.Eq(t, 0, len(n.got)) // no reminder
	sum, err := svc.Summarize()
	assert.NoErr(t, err)
	assert.Eq(t, 0, sum.Due)
	assert.Eq(t, 0, sum.Open)
	list, err := svc.List(jobstore.WorkListOpts{Due: true})
	assert.NoErr(t, err)
	assert.Len(t, list, 0)

	// Reading it directly: never due, health is a plain ok.
	d, err := svc.Detail(c.ID, 10)
	assert.NoErr(t, err)
	assert.False(t, d.Due)
	assert.Eq(t, HealthOK, d.Health)
}

func TestMergeSuggestionsExcludeCaptured(t *testing.T) {
	svc, st, _ := newSvc(t)
	a, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "a"})
	assert.NoErr(t, err)
	b, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "b"})
	assert.NoErr(t, err)
	c := mustCaptured(t, st, "c")

	// A new suggestion that involves a captured item is refused.
	_, _, err = svc.SuggestMerge(a.ID, c.ID, "像", "steward(x)")
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))

	_, _, err = svc.SuggestMerge(a.ID, b.ID, "同一件事", "steward(x)")
	assert.NoErr(t, err)
	got, err := svc.MergeSuggestions()
	assert.NoErr(t, err)
	assert.Len(t, got, 1)

	// A suggestion made before one side was captured is hidden.
	parked := jobstore.WorkCaptured
	_, _, err = st.UpdateWorkItem(b.ID, jobstore.WorkItemPatch{Status: &parked, FromCapture: true}, 0, "human:a")
	assert.NoErr(t, err)
	got, err = svc.MergeSuggestions()
	assert.NoErr(t, err)
	assert.Len(t, got, 0)
}

func TestScanSummarizeSkipsCaptured(t *testing.T) {
	svc, st, w, os := sumFixture(t, goodJSON)
	captured := jobstore.WorkCaptured
	_, _, err := st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{Status: &captured, FromCapture: true}, 0, "human:a")
	assert.NoErr(t, err)

	clk := clockOf(svc)
	clk.Advance(20 * time.Minute) // idle long enough for a tidy-up
	svc.scanSummarize(svc.nowFn())
	svc.WaitIdle()
	assert.Eq(t, 0, os.callCount())
}

func TestCaptureDefaultsKindAndRecordsSource(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, err := svc.Capture(CaptureInput{Text: "随手", SourceSession: "sess-1", SourceJob: "job-1", By: "human:a"})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkKindNote, w.Kind)
	assert.Eq(t, jobstore.WorkCaptured, w.Status)
	assert.Eq(t, jobstore.WorkSourceHuman, w.StatusSource)
	assert.Eq(t, "sess-1", w.CaptureSourceSession)
	assert.Eq(t, "job-1", w.CaptureSourceJob)
	assert.Eq(t, jobstore.WorkCaptured, statusOf(t, st, w.ID))

	_, err = svc.Capture(CaptureInput{Text: "x", Kind: "bogus"})
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
	_, err = svc.Capture(CaptureInput{Text: " "})
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
}

func TestTargetQueryAllowsAnyItem(t *testing.T) {
	svc, st, _ := newSvc(t)
	c, err := svc.Capture(CaptureInput{Text: "导出订单", Project: "p1"})
	assert.NoErr(t, err)
	q, err := svc.TargetQuery(c.ID)
	assert.NoErr(t, err)
	assert.Eq(t, "导出订单", q.Text)
	assert.Eq(t, "p1", q.Project)

	n, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "普通事项", Goal: "目标说明", ProjectKey: "p2"})
	assert.NoErr(t, err)
	q, err = svc.TargetQuery(n.ID)
	assert.NoErr(t, err)
	assert.True(t, strings.Contains(q.Text, "普通事项") && strings.Contains(q.Text, "目标说明"))

	_, err = svc.TargetQuery("w-missing")
	assert.True(t, errors.Is(err, jobstore.ErrWorkItemNotFound))
}
