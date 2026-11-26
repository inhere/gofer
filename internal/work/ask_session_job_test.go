package work

import (
	"context"
	"errors"
	"testing"

	"github.com/gookit/goutil/testutil/assert"
	"github.com/inhere/gofer/internal/jobstore"
)

const acpSessionUUID = "52155a55-0d5b-4c0e-9a77-3b1f0c2d4e66"

func TestAskSessionByAgentSessionIDReachesTheJob(t *testing.T) {
	svc, st, _ := newSvc(t)
	sy := &fakeSayer{}
	svc.SetSessionSayer(sy)
	rec := jobstore.JobRecord{ID: "job-20261126-101010-aaaa", Status: "awaiting_input", Agent: "claude-acp", ProjectKey: "p1", SessionID: acpSessionUUID}
	assert.NoErr(t, st.UpsertJob(rec))
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", SessionIDs: []string{acpSessionUUID}})
	assert.NoErr(t, err)

	res, err := svc.AskSession(context.Background(), AskInput{SessionID: acpSessionUUID, Text: "继续", By: "system"})
	assert.NoErr(t, err)
	assert.Eq(t, "job_say", res.Channel)
	assert.Eq(t, "job", res.Kind)
	assert.Eq(t, rec.ID, sy.id)
	assert.Eq(t, "继续", sy.text)

	// The journal line keeps the UUID short.
	js, _ := st.ListWorkJournal(w.ID, 50, 0)
	assert.True(t, len(js) > 0)

	// An ended job is offline, not "not found".
	rec.Status = "done"
	assert.NoErr(t, st.UpsertJob(rec))
	_, err = svc.AskSession(context.Background(), AskInput{SessionID: acpSessionUUID, Text: "x"})
	assert.True(t, errors.Is(err, ErrAskOffline))

	// A newer running job on the same agent session wins over an ended one.
	rec2 := jobstore.JobRecord{ID: "job-20261126-111111-bbbb", Status: "running", Agent: "claude-acp", ProjectKey: "p1", SessionID: acpSessionUUID}
	assert.NoErr(t, st.UpsertJob(rec2))
	_, err = svc.AskSession(context.Background(), AskInput{SessionID: acpSessionUUID, Text: "y"})
	assert.NoErr(t, err)
	assert.Eq(t, rec2.ID, sy.id)

	_, err = svc.AskSession(context.Background(), AskInput{SessionID: "00000000-unknown", Text: "x"})
	assert.True(t, errors.Is(err, ErrAskNotFound))
}

func TestViewShowsAgentSessionIDJobOnline(t *testing.T) {
	svc, st, _ := newSvc(t)
	rec := jobstore.JobRecord{ID: "job-20261126-101010-aaaa", Status: "awaiting_input", Agent: "claude-acp", ProjectKey: "p1", SessionID: acpSessionUUID}
	assert.NoErr(t, st.UpsertJob(rec))
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", SessionIDs: []string{acpSessionUUID}})
	assert.NoErr(t, err)

	d, err := svc.Detail(w.ID, 0)
	v := d.ItemView
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(v.Sessions))
	b := v.Sessions[0]
	assert.Eq(t, acpSessionUUID, b.SessionID)
	assert.False(t, b.Offline)
	assert.False(t, b.Missing)
	assert.False(t, v.SessionOffline)

	rec.Status = "done"
	assert.NoErr(t, st.UpsertJob(rec))
	d, err = svc.Detail(w.ID, 0)
	v = d.ItemView
	assert.NoErr(t, err)
	assert.True(t, v.Sessions[0].Offline)
}

func TestAskJournalKeepsJobIDWholeAndShortensUUID(t *testing.T) {
	svc, st, _ := newSvc(t)
	svc.SetSessionSayer(&fakeSayer{})
	rec := jobstore.JobRecord{ID: "job-20261126-101010-aaaa", Status: "running", Agent: "claude-acp", ProjectKey: "p1", SessionID: acpSessionUUID}
	assert.NoErr(t, st.UpsertJob(rec))
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t"})
	assert.NoErr(t, err)
	for _, sid := range []string{rec.ID, acpSessionUUID} {
		_, err = svc.AskSession(context.Background(), AskInput{SessionID: sid, WorkID: w.ID, Text: "hi"})
		assert.NoErr(t, err)
	}
	js, _ := st.ListWorkJournal(w.ID, 50, 0)
	var all string
	for _, j := range js {
		all += j.Text + "\n"
	}
	assert.StrContains(t, all, rec.ID)
	assert.StrContains(t, all, "52155a55")
	assert.StrNotContains(t, all, "52155a55-")
}
