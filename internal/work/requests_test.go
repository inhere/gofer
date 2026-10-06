package work

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

type fakeMessenger struct {
	sent []string
	fail error
}

func (f *fakeMessenger) SendRequest(_ context.Context, sid, text, _ string) (string, error) {
	if f.fail != nil {
		return "", f.fail
	}
	f.sent = append(f.sent, sid+"|"+text)
	return "turn", nil
}

func reqFixture(t *testing.T, state string) (*Service, *jobstore.Store, jobstore.WorkItem, *fakeMessenger) {
	t.Helper()
	svc, st, _ := newSvc(t)
	session(t, st, "sess-req00001", state)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "接线", SessionIDs: []string{"sess-req00001"}, By: "human:alice"})
	assert.NoErr(t, err)
	m := &fakeMessenger{}
	svc.SetMessenger(m)
	return svc, st, w, m
}

func TestRequestRunningSessionGoesThroughLedgerAndIsAnswered(t *testing.T) {
	svc, st, w, m := reqFixture(t, jobstore.SessionRunning)
	now := time.Unix(1_800_000_000, 0)
	svc.SetNow(func() time.Time { return now })

	out, err := svc.RequestSessions(context.Background(), w.ID, RequestOpts{By: "human:alice"})
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(out))
	assert.True(t, out[0].Sent)
	assert.Eq(t, jobstore.WorkRequestReport, out[0].Kind)
	assert.Eq(t, jobstore.WorkRequestSent, out[0].State)
	rid := out[0].RequestID
	assert.Eq(t, 1, len(m.sent))
	assert.True(t, strings.Contains(m.sent[0], "--request "+rid))
	r, _, _ := st.GetWorkRequest(rid)
	assert.Eq(t, now.Add(30*time.Minute).Unix(), r.Deadline)

	// The session answers with --request: the ledger row is settled and the fields land.
	_, err = svc.Report(w.ID, ReportInput{Summary: "接好了一半", Next: "测通断", By: "session:sess-req00001", RequestID: rid})
	assert.NoErr(t, err)
	r, _, _ = st.GetWorkRequest(rid)
	assert.Eq(t, jobstore.WorkRequestAnswered, r.State)

	// A report naming someone else's request is refused.
	other, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "别的"})
	_, err = svc.Report(other.ID, ReportInput{Summary: "x", RequestID: rid})
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
	_, err = svc.Report(w.ID, ReportInput{Summary: "x", RequestID: "wr-nope"})
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
}

func TestRequestNotRunningTurnsIntoSummarize(t *testing.T) {
	svc, st, w, m := reqFixture(t, jobstore.SessionEnded)
	out, err := svc.RequestSessions(context.Background(), w.ID, RequestOpts{})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkRequestSummarize, out[0].Kind)
	assert.False(t, out[0].Sent)
	assert.Eq(t, 0, len(m.sent))
	svc.WaitIdle()
	r, _, _ := st.GetWorkRequest(out[0].RequestID)
	assert.Eq(t, jobstore.WorkRequestSummarize, r.Kind)

	// No current session at all.
	empty, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "空"})
	_, err = svc.RequestSessions(context.Background(), empty.ID, RequestOpts{})
	assert.True(t, errors.Is(err, ErrNoSession))
}

func TestRequestDeliveryFailureFallsBackToSummarize(t *testing.T) {
	svc, st, w, m := reqFixture(t, jobstore.SessionRunning)
	m.fail = errors.New("没有传话地址")
	out, err := svc.RequestSessions(context.Background(), w.ID, RequestOpts{})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkRequestSummarize, out[0].Kind)
	assert.True(t, strings.Contains(out[0].Reason, "没有传话地址"))
	reqs, _ := st.ListWorkRequests(w.ID, false, 10)
	assert.Eq(t, 2, len(reqs))
	kinds := map[string]string{}
	for _, r := range reqs {
		kinds[r.Kind] = r.State
	}
	assert.Eq(t, jobstore.WorkRequestFailed, kinds[jobstore.WorkRequestReport])
}

func TestOverdueRequestExpiresAndFallsBackAndSurvivesRestart(t *testing.T) {
	svc, st, w, _ := reqFixture(t, jobstore.SessionRunning)
	base := time.Unix(1_800_000_000, 0)
	svc.SetNow(func() time.Time { return base })
	out, err := svc.RequestSessions(context.Background(), w.ID, RequestOpts{Kind: jobstore.WorkRequestHandoff})
	assert.NoErr(t, err)
	rid := out[0].RequestID

	// A fresh Service over the same store (a restart) carries the timeout on.
	svc2 := New(st)
	svc2.SetMessenger(&fakeMessenger{})
	svc2.SetNow(func() time.Time { return base.Add(10 * time.Minute) })
	svc2.advanceRequests(svc2.nowFn())
	r, _, _ := st.GetWorkRequest(rid)
	assert.Eq(t, jobstore.WorkRequestSent, r.State)

	svc2.SetNow(func() time.Time { return base.Add(31 * time.Minute) })
	svc2.advanceRequests(svc2.nowFn())
	svc2.WaitIdle()
	r, _, _ = st.GetWorkRequest(rid)
	assert.Eq(t, jobstore.WorkRequestExpired, r.State)
	reqs, _ := st.ListWorkRequests(w.ID, false, 10)
	var child *jobstore.WorkRequest
	for i := range reqs {
		if reqs[i].Kind == jobstore.WorkRequestSummarize {
			child = &reqs[i]
		}
	}
	assert.NotNil(t, child)
	assert.Eq(t, rid, child.ParentID)
	j, _ := st.ListWorkJournal(w.ID, 50, 0)
	found := false
	for _, e := range j {
		if strings.Contains(e.Text, "未回应") && strings.Contains(e.Text, "改为整理") {
			found = true
		}
	}
	assert.True(t, found)

	// A late answer to the expired request still settles it.
	_, err = svc2.Report(w.ID, ReportInput{Summary: "迟到的交接", RequestID: rid, By: "session:sess-req00001"})
	assert.NoErr(t, err)
	r, _, _ = st.GetWorkRequest(rid)
	assert.Eq(t, jobstore.WorkRequestAnswered, r.State)
}

func TestParkAndOnsiteAutoHandoffOnlyForRunningSessions(t *testing.T) {
	svc, st, w, m := reqFixture(t, jobstore.SessionRunning)
	_, err := svc.Park(w.ID, 0, "到货后继续", "human:alice")
	assert.NoErr(t, err)
	svc.WaitIdle()
	assert.Eq(t, 1, len(m.sent))
	assert.True(t, strings.Contains(m.sent[0], "交接请求"))

	// needs_onsite via Update also asks; an unchanged status does not ask again.
	onsite := jobstore.WorkNeedsOnsite
	_, err = svc.Update(w.ID, jobstore.WorkItemPatch{Status: &onsite}, 0, "human:alice")
	assert.NoErr(t, err)
	_, err = svc.Update(w.ID, jobstore.WorkItemPatch{Status: &onsite}, 0, "human:alice")
	assert.NoErr(t, err)
	svc.WaitIdle()
	assert.Eq(t, 2, len(m.sent))

	// Switched off by config.
	svc.SetConfigFn(func() config.WorkConfig { f := false; return config.WorkConfig{AutoHandoff: &f} })
	waiting := jobstore.WorkWaitingResource
	_, _ = svc.Update(w.ID, jobstore.WorkItemPatch{Status: &waiting}, 0, "human:alice")
	parked := jobstore.WorkParked
	_, _ = svc.Update(w.ID, jobstore.WorkItemPatch{Status: &parked}, 0, "human:alice")
	svc.WaitIdle()
	assert.Eq(t, 2, len(m.sent))

	// Not running: converted to a tidy-up request, nothing sent.
	svc.SetConfigFn(nil)
	_, _, _ = st.TouchAgentSession("sess-req00001", jobstore.SessionHeartbeat{State: jobstore.SessionEnded})
	_, _ = svc.Update(w.ID, jobstore.WorkItemPatch{Status: &onsite}, 0, "human:alice")
	svc.WaitIdle()
	assert.Eq(t, 2, len(m.sent))
	reqs, _ := st.ListWorkRequests(w.ID, false, 20)
	hasSum := false
	for _, r := range reqs {
		hasSum = hasSum || r.Kind == jobstore.WorkRequestSummarize
	}
	assert.True(t, hasSum)
}
