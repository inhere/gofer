package work

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestStewardUpdateNeverEndsAnItem(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "设备到货", By: "human:a"})
	for _, final := range []string{jobstore.WorkDone, jobstore.WorkDropped} {
		f := final
		_, err := svc.StewardUpdate(w.ID, jobstore.WorkItemPatch{Status: &f}, 0, "steward(claude-acp)")
		assert.True(t, errors.Is(err, ErrStewardFinalStatus))
	}
	cur, _, _ := st.GetWorkItem(w.ID)
	assert.Eq(t, jobstore.WorkActive, cur.Status)
	assert.Eq(t, int64(1), cur.Rev) // refused before anything was written

	bad := "nope"
	_, err := svc.StewardUpdate(w.ID, jobstore.WorkItemPatch{Status: &bad}, 0, "steward(x)")
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
}

func TestStewardUpdateRespectsAPersonsStatus(t *testing.T) {
	svc, st, _ := newSvc(t)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "去现场", By: "human:a"})
	human := jobstore.WorkNeedsOnsite
	_, _, err := st.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{Status: &human}, 0, "human:a")
	assert.NoErr(t, err)

	// The steward wants waiting_resource, but the person's status stands; the rest applies.
	ask, next := jobstore.WorkWaitingResource, "周三带设备过去"
	res, err := svc.StewardUpdate(w.ID, jobstore.WorkItemPatch{Status: &ask, NextStep: &next}, 0, "steward(claude-acp)")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkNeedsOnsite, res.Item.Status)
	assert.Eq(t, next, res.Item.NextStep)
	assert.Eq(t, 1, len(res.Notes))
	assert.True(t, strings.Contains(res.Notes[0], "优先"))

	// A status the steward sets over an automatic one is owned by `report`, not `human`.
	w2, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "等账号", By: "human:a"})
	wr := jobstore.WorkWaitingResource
	res, err = svc.StewardUpdate(w2.ID, jobstore.WorkItemPatch{Status: &wr}, 0, "steward(claude-acp)")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkWaitingResource, res.Item.Status)
	assert.Eq(t, jobstore.WorkSourceReport, res.Item.StatusSource)

	// status_source is not the steward's to set.
	human2 := jobstore.WorkSourceHuman
	res, err = svc.StewardUpdate(w2.ID, jobstore.WorkItemPatch{StatusSource: &human2}, 0, "steward(claude-acp)")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkSourceReport, res.Item.StatusSource)
}

func TestAcceptMergeSuggestionMergesOnlyOnAPersonsYes(t *testing.T) {
	svc, st, _ := newSvc(t)
	a, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "登录修复"})
	b, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "登录问题"})
	sg, stored, err := svc.SuggestMerge(a.ID, b.ID, "同一件事", "steward(claude-acp)")
	assert.NoErr(t, err)
	assert.True(t, stored)
	cur, _, _ := st.GetWorkItem(b.ID)
	assert.Eq(t, "", cur.MergedInto) // a suggestion merges nothing

	pend, _ := svc.MergeSuggestions()
	assert.Eq(t, 1, len(pend))
	w, err := svc.AcceptMergeSuggestion(sg.ID, "human:me")
	assert.NoErr(t, err)
	assert.Eq(t, a.ID, w.ID)
	cur, _, _ = st.GetWorkItem(b.ID)
	assert.Eq(t, a.ID, cur.MergedInto)
	_, err = svc.AcceptMergeSuggestion(sg.ID, "human:me")
	assert.True(t, errors.Is(err, ErrMergeSuggestionResolved))

	c, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "c"})
	d, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "d"})
	sg2, _, _ := svc.SuggestMerge(c.ID, d.ID, "", "steward(x)")
	assert.NoErr(t, svc.DismissMergeSuggestion(sg2.ID))
	cur, _, _ = st.GetWorkItem(d.ID)
	assert.Eq(t, "", cur.MergedInto)
}

type fakeTail struct {
	data []byte
	err  error
	max  int64
}

func (f *fakeTail) ReadTail(_ context.Context, _ jobstore.AgentSession, maxBytes int64) ([]byte, error) {
	f.max = maxBytes
	return f.data, f.err
}

func TestSessionTailReadsTranscriptWithABoundAndFallsBack(t *testing.T) {
	svc, st, _ := newSvc(t)
	_, err := svc.SessionTail(context.Background(), "nope", 0)
	assert.True(t, errors.Is(err, ErrSessionNotFound))

	a, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "s-tail", Agent: "claude", ProjectKey: "p", Cwd: "/ws",
		Transcript: "/x/t.jsonl"})
	assert.NoErr(t, err)
	_, _, err = st.TouchAgentSession(a.SessionID, jobstore.SessionHeartbeat{State: jobstore.SessionIdle, LastMessage: "最后一句", ProgressText: "进度 40%"})
	assert.NoErr(t, err)
	ft := &fakeTail{data: []byte(`{"type":"user","message":{"role":"user","content":"帮我加导出"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"好的，开始"}]}}` + "\n")}
	svc.SetTranscriptSource(ft)

	got, err := svc.SessionTail(context.Background(), a.SessionID, 1<<30)
	assert.NoErr(t, err)
	assert.Eq(t, "transcript", got.Source)
	assert.True(t, strings.Contains(got.Text, "帮我加导出") && strings.Contains(got.Text, "好的，开始"))
	assert.Eq(t, int64(MaxSessionTailBytes), ft.max) // an absurd request is clamped
	_, _ = svc.SessionTail(context.Background(), a.SessionID, 0)
	assert.Eq(t, int64(DefaultSessionTailBytes), ft.max)

	// An unreadable transcript degrades to the session's own last message / progress.
	ft.err = ErrNoTranscript
	got, err = svc.SessionTail(context.Background(), a.SessionID, 0)
	assert.NoErr(t, err)
	assert.Eq(t, "fallback", got.Source)
	assert.True(t, strings.Contains(got.Text, "最后一句") && strings.Contains(got.Text, "进度 40%"))
}

func TestDigestAppendsTheStewardsComment(t *testing.T) {
	svc, st, _ := newSvc(t)
	now := time.Date(2026, 10, 6, 9, 0, 0, 0, time.Local)
	d, err := svc.BuildDigest(now)
	assert.NoErr(t, err)
	assert.Eq(t, "", d.Commentary)
	assert.False(t, strings.Contains(d.Text, "管家点评"))

	id, _ := st.BeginStewardReview("2026-10-06", "daily", []string{"w1"})
	_, err = st.SetStewardReviewSummary("2026-10-06", "三件都在等同一批设备")
	assert.NoErr(t, err)
	// Still running: no comment yet.
	d, _ = svc.BuildDigest(now)
	assert.Eq(t, "", d.Commentary)
	assert.NoErr(t, st.FinishStewardReview(id, jobstore.StewardReviewDone, "", "j1", ""))
	d, _ = svc.BuildDigest(now)
	assert.Eq(t, "三件都在等同一批设备", d.Commentary)
	assert.True(t, strings.HasSuffix(d.Text, "管家点评：三件都在等同一批设备"))

	// Another day's comment never leaks into today's digest.
	d, _ = svc.BuildDigest(now.AddDate(0, 0, 1))
	assert.Eq(t, "", d.Commentary)
}
