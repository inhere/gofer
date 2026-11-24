package seat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

type sentMsg struct{ sid, text, operator, origin string }

type fakeMessenger struct{ sent []sentMsg }

func (f *fakeMessenger) SendMessage(_ context.Context, sid, text, operator, origin string) (jobstore.SessionMessage, error) {
	f.sent = append(f.sent, sentMsg{sid, text, operator, origin})
	return jobstore.SessionMessage{SessionID: sid, Text: text, Status: jobstore.SessionMessageDelivered}, nil
}

type fakeSayer struct{ said [][2]string }

func (f *fakeSayer) SaySession(id, msg string) error {
	f.said = append(f.said, [2]string{id, msg})
	return nil
}

func scheduleEnv(t *testing.T) (*env, *fakeMessenger, *fakeSayer) {
	t.Helper()
	e := newEnv(t)
	m, sy := &fakeMessenger{}, &fakeSayer{}
	e.svc.d.Messenger, e.svc.d.Sayer = m, sy
	return e, m, sy
}

func deliverReq(seatID string) job.SeatDeliverRequest {
	return job.SeatDeliverRequest{SeatID: seatID, Text: "weekly report please", ScheduleID: "sch-1", ScheduleName: "weekly", Origin: "u1"}
}

// A due schedule reaches the session sitting in the seat NOW: after the seat is rebound, the
// new session gets the text and the old one does not.
func TestDeliverToSeatFollowsRebinding(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	e.session(t, "s-old", "/d/work/proj")
	e.session(t, "s-new", "/d/work/proj")
	if _, err := e.svc.Bind(x.ID, "s-old", false, "user"); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultDelivered {
		t.Fatalf("first delivery = %+v, %v", res, err)
	}
	if _, err := e.svc.Bind(x.ID, "s-new", true, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID)); err != nil {
		t.Fatal(err)
	}
	if len(m.sent) != 2 || m.sent[0].sid != "s-old" || m.sent[1].sid != "s-new" {
		t.Fatalf("sent = %+v, want one message to s-old then one to s-new", m.sent)
	}
	if !strings.Contains(m.sent[1].text, "weekly report please") || !strings.Contains(m.sent[1].text, "weekly") || m.sent[1].origin != "u1" {
		t.Fatalf("the message carries the text, the schedule name and the schedule owner: %+v", m.sent[1])
	}
}

// A vacant seat and an offline or ended terminal session take nothing: gofer starts no terminal.
func TestDeliverToSeatOfflineSendsNothing(t *testing.T) {
	e, m, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultOffline {
		t.Fatalf("vacant = %+v, %v", res, err)
	}
	e.session(t, "s1", "/d/work/proj")
	if _, err := e.svc.Bind(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{jobstore.SessionOffline, jobstore.SessionEnded} {
		if _, err := e.st.SetSessionState("s1", state); err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
		if err != nil || res.Outcome != jobstore.ScheduleResultOffline {
			t.Fatalf("%s session = %+v, %v", state, res, err)
		}
	}
	if len(m.sent) != 0 {
		t.Fatalf("nothing may be sent to a session that is not online: %+v", m.sent)
	}
}

func TestDeliverToSeatRefusesArchivedAndUnknown(t *testing.T) {
	e, _, _ := scheduleEnv(t)
	x := e.seat(t, "desk", "")
	if _, err := e.svc.Archive(x.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID)); !errors.Is(err, ErrSeatArchived) {
		t.Fatalf("archived: %v", err)
	}
	if _, err := e.svc.DeliverToSeat(context.Background(), deliverReq("nope")); !errors.Is(err, jobstore.ErrSeatNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func acpSeat(t *testing.T, e *env, status string) jobstore.Seat {
	t.Helper()
	x, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "weekly", Kind: jobstore.SeatKindACP, Agent: "acp", Charter: "be brief"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertJob(jobstore.JobRecord{ID: "job-old", ProjectKey: "proj", Agent: "acp", Runner: "local", Status: status, SessionID: "acp-sess", Cwd: ".", ResultDir: "/r", RequestJSON: "{}", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertJob(jobstore.JobRecord{ID: "job-new", ProjectKey: "proj", Agent: "acp", Runner: "local", Status: job.StatusRunning, SessionID: "acp-sess", Cwd: ".", ResultDir: "/r", RequestJSON: "{}", StartedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Bind(x.ID, "job-old", false, "user"); err != nil {
		t.Fatal(err)
	}
	return x
}

// A live ACP job gets a say; nothing is restarted.
func TestDeliverToSeatACPLiveSays(t *testing.T) {
	e, _, sy := scheduleEnv(t)
	x := acpSeat(t, e, job.StatusAwaitingInput)
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultDelivered {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if len(sy.said) != 1 || sy.said[0][0] != "job-old" || !strings.Contains(sy.said[0][1], "weekly report please") {
		t.Fatalf("said = %+v", sy.said)
	}
	if len(e.rs.calls) != 0 {
		t.Fatalf("a live session must not be restarted: %+v", e.rs.calls)
	}
}

// An ended ACP job is restarted and the text is its opening turn: it is not also said.
func TestDeliverToSeatACPEndedRestartsWithTextOnce(t *testing.T) {
	e, _, sy := scheduleEnv(t)
	x := acpSeat(t, e, job.StatusDone)
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultRestarted || res.JobID != "job-new" {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if len(e.rs.calls) != 1 || !strings.Contains(e.rs.calls[0].Prompt, "weekly report please") {
		t.Fatalf("the opening prompt must carry the text: %+v", e.rs.calls)
	}
	if strings.Count(e.rs.calls[0].Prompt, "weekly report please") != 1 {
		t.Fatalf("the text must appear once: %q", e.rs.calls[0].Prompt)
	}
	if len(sy.said) != 0 {
		t.Fatalf("the text is sent inside the restart only, never also said: %+v", sy.said)
	}
	if got, _ := e.svc.Get(x.ID); got.CurrentRef != "job-new" {
		t.Fatalf("the seat follows the restarted job: %q", got.CurrentRef)
	}
}

// Through the real session relay: the delivered message is a row of the session's outbox, with
// the schedule's text behind the system prefix, and the open turn it answered is settled.
func TestDeliverToSeatStoresTheMessageInTheSessionOutbox(t *testing.T) {
	e := newEnv(t)
	relay := sessionrelay.NewService(e.st)
	relay.ConfigureMessaging(true, "", time.Second, 0)
	e.svc.d.Messenger = relay
	if _, err := relay.Register(sessionrelay.RegisterInput{
		SessionID: "s-rel", Agent: "claude", ProjectKey: "proj", Runner: "server", Cwd: "/d/work/proj", CallerID: "u1", Event: sessionrelay.EventSessionStart,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.SetRelayMode("s-rel", jobstore.RelayModeOn); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.OpenTurn("s-rel", "what next?", 60, false); err != nil {
		t.Fatal(err)
	}
	x := e.seat(t, "desk", "")
	if _, err := e.svc.Bind(x.ID, "s-rel", false, "user"); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.DeliverToSeat(context.Background(), deliverReq(x.ID))
	if err != nil || res.Outcome != jobstore.ScheduleResultDelivered {
		t.Fatalf("res = %+v, %v", res, err)
	}
	msgs, err := e.st.ReadSessionOutbox("s-rel")
	if err != nil {
		t.Fatal(err)
	}
	last := msgs[len(msgs)-1]
	if last.Status != jobstore.SessionMessageDelivered || !strings.Contains(last.Text, "weekly report please") || !strings.Contains(last.Operator, "schedule:sch-1") {
		t.Fatalf("outbox = %+v", msgs)
	}
}
