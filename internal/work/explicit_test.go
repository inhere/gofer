package work

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func allItems(t *testing.T, svc *Service) []ItemView {
	t.Helper()
	items, err := svc.List(jobstore.WorkListOpts{IncludeClosed: true, IncludeMerged: true})
	assert.NoErr(t, err)
	return items
}

// The first report of a session creates its work item, titled by the report's own explicit
// title, never by the prompts the session was given (design 2026-11-01 §4.2).
func TestFirstReportCreatesTheItemWithAnExplicitTitle(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-ex000001", jobstore.SessionRunning)
	svc.OnHumanPrompt(a, "继续")
	svc.OnHumanPrompt(a, "提交 + push")
	assert.Eq(t, 0, len(allItems(t, svc))) // prompts make nothing

	w, created, err := svc.ReportBySession(a.SessionID, SessionReportInput{
		Title:       "导出订单",
		ReportInput: ReportInput{Goal: "把订单导出为 xlsx", Next: "写测试", By: "session:sess-ex000001"},
	})
	assert.NoErr(t, err)
	assert.True(t, created)
	assert.Eq(t, "导出订单", w.Title)
	assert.Eq(t, "把订单导出为 xlsx", w.Goal)
	assert.Eq(t, "写测试", w.NextStep)
	assert.Eq(t, jobstore.WorkOriginReport, w.Source)
	assert.False(t, w.Unsorted)
	assert.Eq(t, "p1", w.ProjectKey)
	assert.Eq(t, "/ws", w.Workspace)
	items := allItems(t, svc)
	assert.Eq(t, 1, len(items))
	assert.Eq(t, []string{a.SessionID}, items[0].SessionIDs)

	// The creation and the report are both journaled.
	d, err := svc.Detail(w.ID, 0)
	assert.NoErr(t, err)
	kinds := map[string]int{}
	for _, e := range d.Journal {
		kinds[e.Kind]++
	}
	assert.Eq(t, 1, kinds[jobstore.WorkJournalReport])
	assert.True(t, kinds[jobstore.WorkJournalStatus] >= 1)

	// The second report reports on the same item.
	w2, created, err := svc.ReportBySession(a.SessionID, SessionReportInput{ReportInput: ReportInput{Summary: "写完了", By: "session:sess-ex000001"}})
	assert.NoErr(t, err)
	assert.False(t, created)
	assert.Eq(t, w.ID, w2.ID)
	assert.Eq(t, "写完了", w2.Summary)
	assert.Eq(t, 1, len(allItems(t, svc)))
}

func TestFirstReportTitleFallsBackToTheGoalAndNeedsOne(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-ex000002", jobstore.SessionRunning)
	b := session(t, st, "sess-ex000003", jobstore.SessionRunning)

	// No title: the first line of the goal (an explicit field), not a prompt.
	w, created, err := svc.ReportBySession(a.SessionID, SessionReportInput{ReportInput: ReportInput{Goal: "修登录页\n细节略"}})
	assert.NoErr(t, err)
	assert.True(t, created)
	assert.Eq(t, "修登录页", w.Title)

	// Neither a title nor a goal: nothing to name the item by, nothing is created.
	_, created, err = svc.ReportBySession(b.SessionID, SessionReportInput{ReportInput: ReportInput{Status: jobstore.WorkNeedsMe}})
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
	assert.False(t, created)
	assert.Eq(t, 1, len(allItems(t, svc)))

	// An empty report, a missing session and an unknown session are refused the same way.
	_, _, err = svc.ReportBySession(b.SessionID, SessionReportInput{Title: "t"})
	assert.True(t, errors.Is(err, ErrEmptyReport))
	_, _, err = svc.ReportBySession("", SessionReportInput{Title: "t", ReportInput: ReportInput{Goal: "g"}})
	assert.True(t, errors.Is(err, ErrSessionNeeded))
	_, _, err = svc.ReportBySession("sess-nope", SessionReportInput{Title: "t", ReportInput: ReportInput{Goal: "g"}})
	assert.True(t, errors.Is(err, ErrSessionNotFound))
	assert.Eq(t, 1, len(allItems(t, svc)))
}

// A session that already belongs to an open item (a person's, a plan's) reports on it and
// creates nothing, whatever title the report carries.
func TestReportBySessionUsesTheSessionsExistingItem(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-ex000004", jobstore.SessionRunning)
	own, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "我建的", SessionIDs: []string{a.SessionID}, By: "human:me"})
	assert.NoErr(t, err)

	w, created, err := svc.ReportBySession(a.SessionID, SessionReportInput{Title: "另一个", ReportInput: ReportInput{Goal: "g"}})
	assert.NoErr(t, err)
	assert.False(t, created)
	assert.Eq(t, own.ID, w.ID)
	assert.Eq(t, "我建的", w.Title)
	assert.Eq(t, 1, len(allItems(t, svc)))
}

// A session that was taken off its item (detached, so only a past session of it) is not "on" it:
// its next report starts a new item instead of writing into the old one.
func TestReportBySessionIgnoresAnItemTheSessionLeft(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-ex000006", jobstore.SessionRunning)
	old, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "旧的", SessionIDs: []string{a.SessionID}, By: "human:me"})
	assert.NoErr(t, err)
	assert.NoErr(t, st.DetachWorkSession(old.ID, a.SessionID, "human:me"))

	w, created, err := svc.ReportBySession(a.SessionID, SessionReportInput{Title: "新的", ReportInput: ReportInput{Goal: "g"}})
	assert.NoErr(t, err)
	assert.True(t, created)
	assert.Eq(t, "新的", w.Title)
	assert.NotEq(t, old.ID, w.ID)
}

// What the model inferred is a reference, not a reason to ping a person: the needs_me push
// carries the structured reason and the words a person or a session wrote, not the
// summarizer's goal / blocker / next.
func TestNeedsMePushLeavesOutModelInferredText(t *testing.T) {
	svc, st, n := newSvc(t)
	on := true
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{NeedsMeNotify: &on} })
	svc.SetNow(func() time.Time { return time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC) })
	a := session(t, st, "sess-ex000005", jobstore.SessionRunning)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", SessionIDs: []string{a.SessionID}, Source: jobstore.WorkOriginAuto})
	assert.NoErr(t, err)
	goal, blocker, next := "模型猜的目标", "模型猜的卡点", "我自己写的下一步"
	_, _, err = st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{Goal: &goal, BlockerText: &blocker, Quiet: true}, 0, SummarizerBy("claude"))
	assert.NoErr(t, err)
	_, _, err = st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{NextStep: &next, Quiet: true}, 0, "human:me")
	assert.NoErr(t, err)

	a.State = jobstore.SessionWaitingReply
	_, err = st.UpsertAgentSession(a)
	assert.NoErr(t, err)
	svc.SyncAll()
	assert.Eq(t, 1, countEvent(n, "work.needs_me"))
	text := n.got[len(n.got)-1].text
	assert.True(t, strings.Contains(text, next), text)
	assert.False(t, strings.Contains(text, goal), text)
	assert.False(t, strings.Contains(text, blocker), text)
}

func TestArchiveAutoOnlyTouchesUntouchedAutoItemsAndIsIdempotent(t *testing.T) {
	svc, st, _ := newSvc(t)
	mk := func(title, project, origin string) jobstore.WorkItem {
		w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: title, ProjectKey: project, Source: origin, By: "system"})
		assert.NoErr(t, err)
		return w
	}
	plain := mk("auto-plain", "p1", jobstore.WorkOriginAuto)
	other := mk("auto-other-project", "p2", jobstore.WorkOriginAuto)
	reported := mk("auto-session-reported", "p1", jobstore.WorkOriginAuto)
	_, err := svc.Report(reported.ID, ReportInput{Summary: "做了一半", By: "session:s-1"}) // a session's words are not a person's edit
	assert.NoErr(t, err)

	touchedStatus := mk("auto-human-status", "p1", jobstore.WorkOriginAuto)
	st1 := jobstore.WorkParked
	_, err = svc.Update(touchedStatus.ID, jobstore.WorkItemPatch{Status: &st1}, 0, "human:me")
	assert.NoErr(t, err)
	touchedNote := mk("auto-human-note", "p1", jobstore.WorkOriginAuto)
	_, err = st.AppendWorkJournal(touchedNote.ID, jobstore.WorkJournalNote, "我看过了", "human:me")
	assert.NoErr(t, err)
	touchedField := mk("auto-human-goal", "p1", jobstore.WorkOriginAuto)
	g := "我定的目标"
	_, err = svc.Update(touchedField.ID, jobstore.WorkItemPatch{Goal: &g}, 0, "human:me")
	assert.NoErr(t, err)

	human := mk("made-by-hand", "p1", jobstore.WorkOriginHuman)
	viaReport := mk("made-by-report", "p1", jobstore.WorkOriginReport)
	capt, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: "随手记", Kind: jobstore.WorkKindIdea, By: "human:me"})
	assert.NoErr(t, err)
	done := mk("auto-already-done", "p1", jobstore.WorkOriginAuto)
	dn := jobstore.WorkDone
	_, _, err = st.UpdateWorkItem(done.ID, jobstore.WorkItemPatch{Status: &dn}, 0, "human:me")
	assert.NoErr(t, err)

	status := func(id string) string {
		w, ok, err := st.GetWorkItem(id)
		assert.NoErr(t, err)
		assert.True(t, ok)
		return w.Status
	}

	// A dry run lists and changes nothing.
	dry, err := svc.ArchiveAuto("human:me", nil, true)
	assert.NoErr(t, err)
	assert.True(t, dry.DryRun)
	assert.Eq(t, 0, dry.Archived)
	assert.Eq(t, 3, len(dry.IDs))
	assert.Eq(t, jobstore.WorkActive, status(plain.ID))

	// A caller restricted to p1 leaves the other project alone.
	res, err := svc.ArchiveAuto("human:me", func(p string) bool { return p == "p1" }, false)
	assert.NoErr(t, err)
	assert.Eq(t, 2, res.Archived)
	assert.Eq(t, jobstore.WorkDropped, status(plain.ID))
	assert.Eq(t, jobstore.WorkDropped, status(reported.ID))
	assert.Eq(t, jobstore.WorkActive, status(other.ID))

	// The rest: unrestricted. Everything a person touched or made stays exactly as it was.
	res, err = svc.ArchiveAuto("human:me", nil, false)
	assert.NoErr(t, err)
	assert.Eq(t, []string{other.ID}, res.IDs)
	assert.Eq(t, jobstore.WorkDropped, status(other.ID))
	assert.Eq(t, jobstore.WorkParked, status(touchedStatus.ID))
	assert.Eq(t, jobstore.WorkActive, status(touchedNote.ID))
	assert.Eq(t, jobstore.WorkActive, status(touchedField.ID))
	assert.Eq(t, jobstore.WorkActive, status(human.ID))
	assert.Eq(t, jobstore.WorkActive, status(viaReport.ID))
	assert.Eq(t, jobstore.WorkCaptured, status(capt.ID))
	assert.Eq(t, jobstore.WorkDone, status(done.ID))

	// Idempotent: nothing is left to archive.
	again, err := svc.ArchiveAuto("human:me", nil, false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, again.Archived)
	assert.Eq(t, 0, len(again.IDs))

	// Every archived item left one audit row (id and actor only) and a journal line saying why.
	rows, err := st.ListAuditEvents(jobstore.WorkArchivedAutoEvent, "")
	assert.NoErr(t, err)
	assert.Eq(t, 3, len(rows))
	for _, r := range rows {
		assert.Eq(t, "human:me", r.Actor)
	}
	d, err := svc.Detail(plain.ID, 0)
	assert.NoErr(t, err)
	why := false
	for _, e := range d.Journal {
		why = why || strings.Contains(e.Text, "Archived") || strings.Contains(e.Text, "归档")
	}
	assert.True(t, why)
}
