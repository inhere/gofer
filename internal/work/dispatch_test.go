package work

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

func TestDispatchToIssueRewrittenTitleKeepsCapturedText(t *testing.T) {
	svc, st, _ := newSvc(t)
	withIssueRepo(t, st)
	const orig = "导出要支持列选择\n第二行：只导出勾选的列，并带上当前筛选"
	w, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: orig, Kind: jobstore.WorkKindIssue,
		ProjectKey: "self", SourceSession: "sess-cap-1", By: "human:cap"})
	assert.NoErr(t, err)

	rec, item, err := svc.DispatchToIssue(w.ID, "r1", "导出列选择", "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkParked, item.Status)
	is := issueOf(t, rec)
	assert.Eq(t, "导出列选择", is.Title)
	assert.True(t, strings.Contains(is.Description, orig), "the captured text must survive a rewritten title: %q", is.Description)
	when := time.Unix(w.CreatedAt, 0).UTC().Format(time.RFC3339)
	assert.True(t, strings.Contains(is.Description, "captured by human:cap at "+when+" from session sess-cap-1, project self"),
		"the source line must name the capturer, the time, the session and the project: %q", is.Description)
	assert.True(t, strings.HasSuffix(strings.TrimSpace(is.Description), "captured as work item "+w.ID),
		"the marker ends the description: %q", is.Description)
}

func TestDispatchToIssueCarriesGoalAndAttachmentRefs(t *testing.T) {
	svc, st, _ := newSvc(t)
	withIssueRepo(t, st)
	const ref = "/v1/attachments/AbCdEfGhIjKlMnOpQrStUv"
	w, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: "页面截图见 " + ref,
		Kind: jobstore.WorkKindIssue, ProjectKey: "self", By: "human:cap"})
	assert.NoErr(t, err)
	goal := "截图里的按钮错位了，小屏上另一个按钮被挡住"
	_, _, err = st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{Goal: &goal}, 0, "human:a")
	assert.NoErr(t, err)

	// The title already is the captured text, so it is not repeated; the goal joins it and the
	// attachment reference gets a line of its own.
	rec, _, err := svc.DispatchToIssue(w.ID, "r1", "", "human:a")
	assert.NoErr(t, err)
	is := issueOf(t, rec)
	assert.Eq(t, "页面截图见 "+ref, is.Title)
	assert.True(t, strings.Contains(is.Description, goal), "the goal must be in the description: %q", is.Description)
	assert.True(t, strings.Contains(is.Description, "attachments: "+ref), "the attachment reference needs its own line: %q", is.Description)
	assert.True(t, strings.Contains(is.Description, "captured by human:cap at "), "%q", is.Description)
}

// TestDispatchToIssueSourceWithoutSession: an item the mailbox recorded has no source session; the
// description still says who and where it came from.
func TestDispatchToIssueSourceWithoutSession(t *testing.T) {
	svc, st, _ := newSvc(t)
	withIssueRepo(t, st)
	const text = "邮件 #7 待指派：session:zz 没有这个会话"
	w, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: text, Kind: jobstore.WorkKindNote, By: "mail"})
	assert.NoErr(t, err)

	rec, _, err := svc.DispatchToIssue(w.ID, "r1", "信件无人接收", "human:a")
	assert.NoErr(t, err)
	is := issueOf(t, rec)
	assert.True(t, strings.Contains(is.Description, text), "%q", is.Description)
	assert.True(t, strings.Contains(is.Description, "captured by mail at "), "%q", is.Description)
	assert.True(t, strings.Contains(is.Description, "from the mailbox"), "%q", is.Description)
}

// TestDispatchToIssueTitleOnlyIsNotCopied: an item whose title IS the issue title adds nothing to
// the description besides the source line and the marker.
func TestDispatchToIssueTitleOnlyIsNotCopied(t *testing.T) {
	svc, st, _ := newSvc(t)
	withIssueRepo(t, st)
	got := mustCaptured(t, st, "faster export")

	rec, _, err := svc.DispatchToIssue(got.ID, "r1", "", "human:a")
	assert.NoErr(t, err)
	is := issueOf(t, rec)
	assert.Eq(t, "faster export", is.Title)
	assert.False(t, strings.Contains(is.Description, "faster export"), "the title must not be copied: %q", is.Description)
	assert.True(t, strings.HasPrefix(is.Description, "captured by human:a at "), "%q", is.Description)
}

func withIssueRepo(t *testing.T, st *jobstore.Store) {
	t.Helper()
	assert.NoErr(t, st.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "r1", ProjectKey: "self", Prefix: "zz"}))
}

func issueOf(t *testing.T, rec jobstore.TrackerRecord) tracker.Issue {
	t.Helper()
	var is tracker.Issue
	assert.NoErr(t, json.Unmarshal(rec.Body, &is))
	return is
}

func TestDispatchOnlyCapturedItems(t *testing.T) {
	svc, st, _ := newSvc(t)
	c := mustCaptured(t, st, "想法")

	got, err := svc.Captured(c.ID)
	assert.NoErr(t, err)
	assert.Eq(t, "想法", got.Text)

	_, err = svc.Captured("w-nope")
	assert.True(t, errors.Is(err, jobstore.ErrWorkItemNotFound))

	w, err := svc.DispatchClose(c.ID, DispatchArchive, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkDone, w.Status)
	j, err := st.ListWorkJournal(c.ID, 20, 0)
	assert.NoErr(t, err)
	found := false
	for _, e := range j {
		found = found || strings.Contains(e.Text, "归档")
	}
	assert.True(t, found)

	// It left the capture box: every further dispatch is ErrNotCaptured.
	_, err = svc.DispatchClose(c.ID, DispatchDrop, "human:a")
	assert.True(t, errors.Is(err, ErrNotCaptured))
	_, _, err = svc.DispatchToTodo(c.ID, ToTodoInput{By: "human:a"})
	assert.True(t, errors.Is(err, ErrNotCaptured))
	_, err = svc.FinishDispatch(c.ID, jobstore.WorkActive, "", "", "", "by:x")
	assert.True(t, errors.Is(err, ErrNotCaptured))

	d := mustCaptured(t, st, "另一个")
	_, err = svc.DispatchClose(d.ID, "bogus", "human:a")
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
	assert.Eq(t, jobstore.WorkCaptured, statusOf(t, st, d.ID))
}
