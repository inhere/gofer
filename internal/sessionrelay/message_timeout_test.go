package sessionrelay

// gofer-kx53: the one-shot messenger job is the same kind of delivery as path A/C, so
// outliving messengerTimeout must not write a dead 「失败」 — a job that began is
// 「送达中」 and settles at its terminal state; one that never began is cancelled and
// reported 「未送达」.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// slowMessenger never finishes inside the timeout; abandon answers what the host found
// when it gave up, and state is what the probe reports afterwards.
type slowMessenger struct {
	mu        sync.Mutex
	abandon   InjectResult
	abandoned []string
	state     InjectState
}

func (f *slowMessenger) SubmitMessenger(string, string, string, []string, string, string) (string, error) {
	return "job-msgr", nil
}
func (*slowMessenger) MessengerJob(string) (bool, string, int, string, error) {
	return false, "running", 0, "", nil
}
func (f *slowMessenger) AbandonMessengerJob(id string) (InjectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abandoned = append(f.abandoned, id)
	return f.abandon, nil
}
func (f *slowMessenger) InjectJob(string) (InjectState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}
func (f *slowMessenger) setState(st InjectState) {
	f.mu.Lock()
	f.state = st
	f.mu.Unlock()
}

func messengerSession(t *testing.T, s *Service, sid string) {
	t.Helper()
	_, err := s.Register(RegisterInput{SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: "builder",
		PeerName: "proj-x", PeerMessaging: true, Event: EventSessionStart})
	assert.NoErr(t, err)
	_, err = s.Heartbeat(sid, HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
}

func newSlowMessengerSvc(t *testing.T, f *slowMessenger) *Service {
	t.Helper()
	s := newSvc(t)
	s.ConfigureMessaging(true, "", 80*time.Millisecond, 0) // the timeout under test
	s.SetMessenger(f)
	s.SetInjectProbe(f)
	return s
}

func TestMessengerTimeoutRunningJobIsDeliveringThenSettles(t *testing.T) {
	f := &slowMessenger{abandon: InjectResult{JobID: "job-msgr", ExitCode: -1, Running: true, Status: "running"}}
	s := newSlowMessengerSvc(t, f)
	messengerSession(t, s, "sid-m-run")

	m, err := s.SendMessage(context.Background(), "sid-m-run", "hello", "alice")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionMessageDelivering, m.Status)
	assert.Eq(t, "job-msgr", m.JobID)
	assert.Eq(t, "messenger", m.Channel)

	// Still running: stays 「送达中」.
	f.setState(InjectState{Known: true, Status: "running"})
	assert.NoErr(t, s.ReconcileDeliveries("sid-m-run"))
	assert.Eq(t, jobstore.SessionMessageDelivering, outboxLatest(t, s, "sid-m-run", m.ID).Status)

	// Terminal with the receipt: written back through the terminal-hook link.
	f.setState(InjectState{Known: true, Done: true, Status: "done", ExitCode: 0, Output: "已发送"})
	settled, err := s.ReconcileDeliveryJob("job-msgr")
	assert.NoErr(t, err)
	assert.True(t, settled)
	wait.Until(t, wait.Timeout(t, 5*time.Second), "messenger delivery written back", func() bool {
		return outboxLatest(t, s, "sid-m-run", m.ID).Status == jobstore.SessionMessageDelivered
	})
}

func TestMessengerTimeoutLateFailureIsWrittenBack(t *testing.T) {
	f := &slowMessenger{abandon: InjectResult{JobID: "job-msgr", ExitCode: -1, Running: true, Status: "running"}}
	s := newSlowMessengerSvc(t, f)
	messengerSession(t, s, "sid-m-fail")
	m, err := s.SendMessage(context.Background(), "sid-m-fail", "hello", "alice")
	assert.NoErr(t, err)

	// exit 0 without the receipt is not a delivery.
	f.setState(InjectState{Known: true, Done: true, Status: "done", ExitCode: 0, Output: "no such agent"})
	assert.NoErr(t, s.ReconcileDeliveries("sid-m-fail"))
	final := outboxLatest(t, s, "sid-m-fail", m.ID)
	assert.Eq(t, jobstore.SessionMessageFailed, final.Status)
	assert.Contains(t, final.Error, "no such agent")
}

func TestMessengerTimeoutUnstartedJobIsCancelledAndUndelivered(t *testing.T) {
	f := &slowMessenger{abandon: InjectResult{JobID: "job-msgr", ExitCode: -1, Status: "queued", StatusDetail: "排队中"}}
	s := newSlowMessengerSvc(t, f)
	messengerSession(t, s, "sid-m-queued")

	m, err := s.SendMessage(context.Background(), "sid-m-queued", "hello", "alice")
	assert.Err(t, err)
	assert.Eq(t, []string{"job-msgr"}, f.abandoned)
	assert.Eq(t, jobstore.SessionMessageUndelivered, m.Status)
	assert.Contains(t, m.Error, "排队中")
	assert.Eq(t, jobstore.SessionMessageUndelivered, outboxLatest(t, s, "sid-m-queued", m.ID).Status)
}

func TestMessengerTimeoutJobFinishedMeanwhile(t *testing.T) {
	f := &slowMessenger{abandon: InjectResult{JobID: "job-msgr", ExitCode: 0, Output: "已发送"}}
	s := newSlowMessengerSvc(t, f)
	messengerSession(t, s, "sid-m-done")
	m, err := s.SendMessage(context.Background(), "sid-m-done", "hello", "alice")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionMessageDelivered, m.Status)
}

// A messenger that cannot settle a job keeps the plain timeout failure.
func TestMessengerTimeoutWithoutAbandonerFails(t *testing.T) {
	s := newSvc(t)
	s.ConfigureMessaging(true, "", 80*time.Millisecond, 0)
	s.SetMessenger(plainSlowMessenger{})
	messengerSession(t, s, "sid-m-plain")
	m, err := s.SendMessage(context.Background(), "sid-m-plain", "hello", "alice")
	assert.Err(t, err)
	assert.Eq(t, jobstore.SessionMessageFailed, m.Status)
	assert.Contains(t, m.Error, "超时")
}

type plainSlowMessenger struct{}

func (plainSlowMessenger) SubmitMessenger(string, string, string, []string, string, string) (string, error) {
	return "job-plain", nil
}
func (plainSlowMessenger) MessengerJob(string) (bool, string, int, string, error) {
	return false, "running", 0, "", nil
}
