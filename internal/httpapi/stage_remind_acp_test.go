package httpapi

import (
	"context"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/stage"
)

type remindSayer struct{ id, text string }

func (r *remindSayer) SaySession(id, text string) error { r.id, r.text = id, text; return nil }

// A flow registered under a resident ACP job's own agent session id still gets its remind
// "continue" delivered, through `job say` to the job.
func TestStageRemindReachesJobByAgentSessionID(t *testing.T) {
	s := newTestServer(t, testToken, false)
	st := s.jobs.Meta()
	const uuid = "52155a55-0d5b-4c0e-9a77-3b1f0c2d4e66"
	const jobID = "job-20261126-101010-ab12cd34"
	if err := st.UpsertJob(jobstore.JobRecord{ID: jobID, Status: "awaiting_input", Agent: "claude-acp", ProjectKey: "self", SessionID: uuid}); err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", SessionIDs: []string{uuid}})
	if err != nil {
		t.Fatal(err)
	}
	sy := &remindSayer{}
	s.work.SetSessionSayer(sy)

	out, err := s.deliverStageRemind(context.Background(), stage.Delivery{WorkItemID: w.ID, SessionID: uuid, Step: "verify", Text: "continue"})
	if err != nil || out != stage.DeliveredNow {
		t.Fatalf("deliver = %q %v, want delivered", out, err)
	}
	if sy.id != jobID || sy.text != "continue" {
		t.Fatalf("sayer got %q %q, want the job id", sy.id, sy.text)
	}
}
