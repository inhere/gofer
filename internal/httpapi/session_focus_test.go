package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestSessionFocusListsOwnWork is the compact / resume re-injection source: one call
// answers what this session supervises.
func TestSessionFocusListsOwnWork(t *testing.T) {
	s := topicServer(t)
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "sess-a", CallerID: "alice"})

	meta := s.jobs.Meta()
	if err := meta.InsertPlan(jobstore.Plan{PlanID: "p-1", Title: "会话收尾", Status: jobstore.PlanOpen, SupervisorSessionID: "sess-a"}); err != nil {
		t.Fatal(err)
	}
	for _, todo := range []jobstore.PlanTodo{
		{TodoID: "t-1", PlanID: "p-1", Title: "写测试", Done: true},
		{TodoID: "t-2", PlanID: "p-1", Title: "写文档"},
	} {
		if err := meta.InsertTodo(todo); err != nil {
			t.Fatal(err)
		}
	}
	// A plan supervised by another session must not show up.
	if err := meta.InsertPlan(jobstore.Plan{PlanID: "p-other", Title: "别人的 plan", Status: jobstore.PlanOpen, SupervisorSessionID: "sess-other"}); err != nil {
		t.Fatal(err)
	}
	letter, err := meta.InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindCaller, FromCaller: "bob", ToKind: jobstore.MailKindSession, ToID: "sess-a", Body: "先看这个\n第二行",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := meta.ClaimSessionMail("sess-a", jobstore.TakeMailOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.ConfirmSessionMail("sess-a", []int64{letter.ID}); err != nil {
		t.Fatal(err)
	}

	resp := do(t, s, http.MethodGet, "/v1/sessions/sess-a/focus", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var view sessionFocusView
	decode(t, resp, &view)
	if view.Caller != "alice" || view.StartedAt == 0 || view.Agent != "claude" {
		t.Fatalf("session fields missing: %+v", view)
	}
	if len(view.Plans) != 1 || view.Plans[0].PlanID != "p-1" {
		t.Fatalf("plans = %+v, want only p-1", view.Plans)
	}
	if view.Plans[0].Total != 2 || view.Plans[0].Progress != 1 || view.Plans[0].Next != "写文档" {
		t.Fatalf("plan progress = %+v", view.Plans[0])
	}
	if len(view.Mail) != 1 || view.Mail[0].ID != letter.ID || view.Mail[0].Subject != "先看这个" {
		t.Fatalf("mail = %+v", view.Mail)
	}
	if view.Mail[0].From == "" {
		t.Fatalf("mail sender display missing: %+v", view.Mail[0])
	}
}

// A session that is not the caller's needs session.read.others, like every other
// session read.
func TestSessionFocusOtherSessionDenied(t *testing.T) {
	s := topicServer(t)
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "sess-a", CallerID: "alice"})

	resp := do(t, s, http.MethodGet, "/v1/sessions/sess-a/focus", "tok-bob", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for another caller's session", resp.StatusCode)
	}
}
