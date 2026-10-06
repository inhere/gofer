package work

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func mkPlan(t *testing.T, st *jobstore.Store, id string) {
	t.Helper()
	now := time.Now().Unix()
	assert.NoErr(t, st.InsertPlan(jobstore.Plan{PlanID: id, Title: id, Status: jobstore.PlanOpen, CreatedAt: now, UpdatedAt: now}))
}

func TestPlanDecisionMakesNeedsMeAndAnswerReleasesIt(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-dec00001", jobstore.SessionRunning)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", SessionIDs: []string{a.SessionID}, Source: jobstore.WorkOriginAuto})
	assert.NoErr(t, err)
	mkPlan(t, st, "plan-x")
	_, err = st.AddWorkLink(w.ID, jobstore.WorkLinkPlan, "plan-x", "h")
	assert.NoErr(t, err)

	svc.SyncAll()
	got, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkActive, got.Status)

	d := &jobstore.PlanDecision{ID: "dec-1", PlanID: "plan-x", Title: "选哪个方案", Question: "A or B"}
	assert.NoErr(t, st.InsertDecision(d))
	svc.SyncAll()
	got, _, _ = st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkNeedsMe, got.Status)

	ok, err := st.AnswerDecision("dec-1", "A", "h")
	assert.NoErr(t, err)
	assert.True(t, ok)
	svc.SyncAll()
	got, _, _ = st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkActive, got.Status) // back to the session's mapping
}

func TestJobPlanDecisionAndOtherPlanIgnored(t *testing.T) {
	svc, st, _ := newSvc(t)
	a := session(t, st, "sess-dec00002", jobstore.SessionRunning)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", SessionIDs: []string{a.SessionID}, Source: jobstore.WorkOriginAuto})
	_, err := st.AddWorkLink(w.ID, jobstore.WorkLinkJob, "job-9", "h")
	assert.NoErr(t, err)
	svc.SetJobProbe(fakeProbe{"job-9": {Status: "running", PlanID: "plan-job"}})
	mkPlan(t, st, "plan-job")
	mkPlan(t, st, "plan-other")
	assert.NoErr(t, st.InsertDecision(&jobstore.PlanDecision{ID: "dec-o", PlanID: "plan-other", Title: "无关", Question: "q"}))
	svc.SyncAll()
	got, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkActive, got.Status)

	assert.NoErr(t, st.InsertDecision(&jobstore.PlanDecision{ID: "dec-j", PlanID: "plan-job", Title: "关联 job 所属 plan", Question: "q"}))
	svc.SyncAll()
	got, _, _ = st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkNeedsMe, got.Status)
}

func TestCompletionWriteBackSuggestsNeverChangesStatus(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "做完它", Source: jobstore.WorkOriginHuman})
	mkPlan(t, st, "plan-c")
	now := time.Now().Unix()
	assert.NoErr(t, st.InsertTodo(jobstore.PlanTodo{TodoID: "todo-1", PlanID: "plan-c", Title: "a", Status: jobstore.TodoPending, CreatedAt: now, UpdatedAt: now}))
	assert.NoErr(t, st.InsertTodo(jobstore.PlanTodo{TodoID: "todo-2", PlanID: "plan-c", Title: "b", Status: jobstore.TodoPending, CreatedAt: now, UpdatedAt: now}))
	_, _ = st.AddWorkLink(w.ID, jobstore.WorkLinkTodo, "todo-1", "h")
	_, _ = st.AddWorkLink(w.ID, jobstore.WorkLinkTodo, "todo-2", "h")
	before, _, _ := st.GetWorkItem(w.ID)

	_, err := st.SetTodoDone("todo-1", true)
	assert.NoErr(t, err)
	svc.SyncAll()
	_, found, _ := st.GetWorkSuggestion(w.ID, jobstore.SuggestStatusHint)
	assert.False(t, found) // todo-2 still open

	_, err = st.SetTodoDone("todo-2", true)
	assert.NoErr(t, err)
	svc.SyncAll()
	sg, found, _ := st.GetWorkSuggestion(w.ID, jobstore.SuggestStatusHint)
	assert.True(t, found)
	assert.Eq(t, jobstore.WorkReview, sg.Value)
	after, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, before.Status, after.Status) // the person decides

	// No journal spam on the next sweep.
	n1 := len(journalTexts(t, st, w.ID))
	svc.SyncAll()
	assert.Eq(t, n1, len(journalTexts(t, st, w.ID)))

	// Adopting is the person's own write.
	got, err := svc.AcceptSuggestion(w.ID, jobstore.SuggestStatusHint, "human:me")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkReview, got.Status)
	assert.Eq(t, jobstore.WorkSourceHuman, got.StatusSource)
}

func journalTexts(t *testing.T, st *jobstore.Store, id string) []string {
	t.Helper()
	js, err := st.ListWorkJournal(id, 100, 0)
	assert.NoErr(t, err)
	var out []string
	for _, j := range js {
		out = append(out, j.Text)
	}
	return out
}

func TestClosedIssueSuggestsDoneAndDismissedIsNotRepeated(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "修 bug", Source: jobstore.WorkOriginHuman})
	_, _ = st.AddWorkLink(w.ID, jobstore.WorkLinkIssue, "g-1", "h")
	body := func(status string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"id": "g-1", "title": "x", "status": status})
		return b
	}
	assert.NoErr(t, st.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: "t1", ID: "g-1", Body: body("open"), Rev: 1}))
	svc.SyncAll()
	_, found, _ := st.GetWorkSuggestion(w.ID, jobstore.SuggestStatusHint)
	assert.False(t, found)

	assert.NoErr(t, st.UpsertTrackerIssue(jobstore.TrackerRecord{TrackerID: "t1", ID: "g-1", Body: body("closed"), Rev: 2}))
	svc.SyncAll()
	sg, found, _ := st.GetWorkSuggestion(w.ID, jobstore.SuggestStatusHint)
	assert.True(t, found)
	assert.Eq(t, jobstore.WorkDone, sg.Value)

	assert.NoErr(t, svc.DismissSuggestion(w.ID, jobstore.SuggestStatusHint, "human:me"))
	svc.SyncAll()
	_, found, _ = st.GetWorkSuggestion(w.ID, jobstore.SuggestStatusHint)
	assert.False(t, found)
}

func TestNeedsMeNotifyOffByDefaultOnWithThrottle(t *testing.T) {
	svc, st, n := newSvc(t)
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	svc.SetNow(func() time.Time { return now })
	a := session(t, st, "sess-nm000001", jobstore.SessionRunning)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "等我的", SessionIDs: []string{a.SessionID}, Source: jobstore.WorkOriginAuto})
	flip := func(state string) {
		a2 := a
		a2.State = state
		_, err := st.UpsertAgentSession(a2)
		assert.NoErr(t, err)
		svc.SyncAll()
	}

	flip(jobstore.SessionWaitingReply) // switch off: silent
	assert.Eq(t, 0, countEvent(n, "work.needs_me"))

	on := true
	svc.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{NeedsMeNotify: &on} })
	flip(jobstore.SessionRunning)
	got, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkActive, got.Status)
	flip(jobstore.SessionWaitingReply)
	assert.Eq(t, 1, countEvent(n, "work.needs_me"))

	// Within 30 minutes: throttled.
	now = now.Add(10 * time.Minute)
	flip(jobstore.SessionRunning)
	flip(jobstore.SessionWaitingReply)
	assert.Eq(t, 1, countEvent(n, "work.needs_me"))

	// After the window: announced again.
	now = now.Add(31 * time.Minute)
	flip(jobstore.SessionRunning)
	flip(jobstore.SessionWaitingReply)
	assert.Eq(t, 2, countEvent(n, "work.needs_me"))

	// A manual status write counts too (a different item, own throttle).
	w2, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "手动", Source: jobstore.WorkOriginHuman})
	st2, src := jobstore.WorkNeedsMe, jobstore.WorkSourceHuman
	_, err := svc.Update(w2.ID, jobstore.WorkItemPatch{Status: &st2, StatusSource: &src}, 0, "human:me")
	assert.NoErr(t, err)
	assert.Eq(t, 3, countEvent(n, "work.needs_me"))
}
