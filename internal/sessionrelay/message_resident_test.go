package sessionrelay

import (
	"context"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// residentFake implements both messenger seams and records which one was used.
type residentFake struct {
	residentCalls []string // "<runner>|<target>"
	submitted     int
}

func (f *residentFake) SubmitMessenger(string, string, string, []string, string, string) (string, error) {
	f.submitted++
	return "job-1", nil
}
func (*residentFake) MessengerJob(string) (bool, string, int, string, error) {
	return true, "done", 0, "已发送", nil
}
func (f *residentFake) SendMessengerResident(_ context.Context, runner, _, target string, _ []string) (string, error) {
	f.residentCalls = append(f.residentCalls, runner+"|"+target)
	return "已发送", nil
}

// A hook-registered session on the server's own machine carries the runner label
// "server"; it must use the resident messenger (no delivery job), keyed by the
// canonical "local". A worker session still goes through a job.
func TestSendMessageUsesResidentMessengerForServerLabel(t *testing.T) {
	for _, label := range []string{"server", "local", "Server"} {
		s := newSvc(t)
		f := &residentFake{}
		s.SetMessenger(f)
		_, err := s.Register(RegisterInput{SessionID: "sid-" + label, Agent: "claude", ProjectKey: "self", Runner: label,
			PeerName: "proj-a", PeerMessaging: true, Event: EventSessionStart})
		assert.NoErr(t, err)
		_, err = s.Heartbeat("sid-"+label, HeartbeatInput{Event: EventStop})
		assert.NoErr(t, err)
		m, err := s.SendMessage(context.Background(), "sid-"+label, "hi", "alice")
		assert.NoErr(t, err)
		assert.Eq(t, jobstore.SessionMessageDelivered, m.Status)
		assert.Eq(t, "messenger", m.Channel)
		assert.Eq(t, []string{"local|proj-a"}, f.residentCalls, label)
		assert.Eq(t, 0, f.submitted, label)
	}

	s := newSvc(t)
	f := &residentFake{}
	s.SetMessenger(f)
	_, err := s.Register(RegisterInput{SessionID: "sid-worker", Agent: "claude", ProjectKey: "self", Runner: "builder",
		PeerName: "proj-b", PeerMessaging: true, Event: EventSessionStart})
	assert.NoErr(t, err)
	_, err = s.Heartbeat("sid-worker", HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
	_, err = s.SendMessage(context.Background(), "sid-worker", "hi", "alice")
	assert.NoErr(t, err)
	assert.Len(t, f.residentCalls, 0)
	assert.Eq(t, 1, f.submitted)
}
