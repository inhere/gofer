package sessionrelay

// gofer-qzv5: a delivery whose internal job outlives gofer's wait window — a server
// update waiting for idle parks new jobs for minutes — is not a failure. These tests
// pin the three answers: the text is on its way (「送达中」, settled at the job's terminal
// state), it never started (「未送达」, cancelled, safe to retry), and the reason a human
// reads says WHY instead of an exit code nobody produced.

import (
	"context"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// pendingInjector answers every injection as "the job had begun but had not finished
// when the wait window elapsed" (InjectResult.Running) and offers the probe state the
// test scripts (InjectProbe).
type pendingInjector struct {
	reqs  []InjectRequest
	state InjectState
	// unknown makes the probe report a job record that is gone (Known=false).
	unknown bool
	probed  []string
}

func (f *pendingInjector) InjectSession(_ context.Context, req InjectRequest) (InjectResult, error) {
	f.reqs = append(f.reqs, req)
	return InjectResult{JobID: "job-pending", ExitCode: -1, Running: true, Status: "running"}, nil
}

func (f *pendingInjector) InjectJob(jobID string) (InjectState, error) {
	f.probed = append(f.probed, jobID)
	if f.unknown {
		return InjectState{}, nil
	}
	return f.state, nil
}

// outboxLatest returns the newest state of one message.
func outboxLatest(t *testing.T, s *Service, sid, id string) jobstore.SessionMessage {
	t.Helper()
	list, err := s.store.ReadSessionOutbox(sid)
	assert.NoErr(t, err)
	for _, m := range list {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("message %s not found in %s's outbox", id, sid)
	return jobstore.SessionMessage{}
}

// TestSendMessageRecordsDeliveringAndSettlesIt: the delivery job outlived the wait
// window, so the message says 「送达中」 instead of 「失败 · exit code -1」 — and when the job
// ends, its real outcome is written back onto that record.
func TestSendMessageRecordsDeliveringAndSettlesIt(t *testing.T) {
	s := newSvc(t)
	inj := &pendingInjector{}
	s.SetInjector(inj)
	s.SetInjectProbe(inj)
	s.SetDeliverPlanner(planFor(true))
	cmdSession(t, s, "sid-pending-ok", "%1")

	m, err := s.SendMessage(context.Background(), "sid-pending-ok", "hello", "alice", "alice")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionMessageDelivering, m.Status)
	assert.Eq(t, "job-pending", m.JobID)
	assert.Eq(t, PathCommand, m.Channel)
	assert.Len(t, inj.reqs, 1)
	assert.True(t, inj.reqs[0].UpdateExempt, "a delivery job must not be parked by an update")

	// The job is still carrying the text: the record stays 「送达中」.
	inj.state = InjectState{Known: true, Status: "running"}
	assert.NoErr(t, s.ReconcileDeliveries("sid-pending-ok"))
	assert.Eq(t, jobstore.SessionMessageDelivering, outboxLatest(t, s, "sid-pending-ok", m.ID).Status)

	// ...and when it ends well, the delivery is written back through the same link the
	// assembly's terminal hook uses.
	inj.state = InjectState{Known: true, Done: true, Status: "done", ExitCode: 0}
	settled, err := s.ReconcileDeliveryJob("job-pending")
	assert.NoErr(t, err)
	assert.True(t, settled)
	final := outboxLatest(t, s, "sid-pending-ok", m.ID)
	assert.Eq(t, jobstore.SessionMessageDelivered, final.Status)
	assert.Eq(t, "", final.Error)

	// The link is spent: a second terminal event is not this message's business.
	settled, err = s.ReconcileDeliveryJob("job-pending")
	assert.NoErr(t, err)
	assert.False(t, settled)
}

// TestDeliverWriteBackKeepsTheJobStatusInTheReason: a delivery that ran and failed is
// `failed` (it may have landed), and the reason names the job — its status, its exit
// code and its output — instead of the bare exit code -1 the reporter saw.
func TestDeliverWriteBackKeepsTheJobStatusInTheReason(t *testing.T) {
	s := newSvc(t)
	inj := &pendingInjector{state: InjectState{Known: true, Done: true, Status: "failed", ExitCode: 5,
		Output: "myagent: cannot reach the live process\n"}}
	s.SetInjector(inj)
	s.SetInjectProbe(inj)
	s.SetDeliverPlanner(planFor(true))
	cmdSession(t, s, "sid-pending-fail", "%1")

	m, err := s.SendMessage(context.Background(), "sid-pending-fail", "hello", "alice", "alice")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionMessageDelivering, m.Status)

	assert.NoErr(t, s.ReconcileDeliveries("sid-pending-fail"))
	final := outboxLatest(t, s, "sid-pending-fail", m.ID)
	assert.Eq(t, jobstore.SessionMessageFailed, final.Status)
	assert.Contains(t, final.Error, "job job-pending 状态 failed exit=5")
	assert.Contains(t, final.Error, "cannot reach the live process")
}

// TestDeliverWriteBackReportsAJobThatIsGone: after a server restart the delivery job's
// record may be gone — the message must not stay 「送达中」 for ever.
func TestDeliverWriteBackReportsAJobThatIsGone(t *testing.T) {
	s := newSvc(t)
	inj := &pendingInjector{unknown: true}
	s.SetInjector(inj)
	s.SetInjectProbe(inj)
	s.SetDeliverPlanner(planFor(true))
	cmdSession(t, s, "sid-pending-gone", "%1")

	m, err := s.SendMessage(context.Background(), "sid-pending-gone", "hello", "alice", "alice")
	assert.NoErr(t, err)
	assert.NoErr(t, s.ReconcileDeliveries("sid-pending-gone"))
	final := outboxLatest(t, s, "sid-pending-gone", m.ID)
	assert.Eq(t, jobstore.SessionMessageFailed, final.Status)
	assert.Contains(t, final.Error, "无法确认是否送达")
}

// TestDeliverUnstartedIsUndelivered: the job had NOT started when the wait window
// elapsed (an update waiting for idle is the case that motivated this): nothing was
// delivered, nothing can arrive late, and the reason says which status held it — so the
// web may offer a retry.
func TestDeliverUnstartedIsUndelivered(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{res: InjectResult{JobID: "job-parked", ExitCode: -1,
		Status: "paused_for_update", StatusDetail: "升级等待（新任务暂停启动）"}}
	s.SetInjector(inj)
	s.SetDeliverPlanner(planFor(true))
	cmdSession(t, s, "sid-unstarted", "%1")

	_, derr := s.Deliver(context.Background(), "sid-unstarted", "hello", "alice", "alice", false)
	assert.Err(t, derr)
	assert.Eq(t, NotDeliveredPrefix+"paused_for_update", DeliverReason(derr))

	m, err := s.SendMessage(context.Background(), "sid-unstarted", "hello", "alice", "alice")
	assert.Err(t, err)
	assert.Eq(t, jobstore.SessionMessageUndelivered, m.Status)
	assert.Contains(t, m.Error, "not_delivered:paused_for_update")
	assert.Contains(t, m.Error, "升级等待（新任务暂停启动）")
	assert.Contains(t, m.Error, "未送达")
}

// TestDeliveryNeverStartedSet pins WHICH failures a retry is safe against: only the ones
// where no delivery was attempted. A deliver_command that exited non-zero may still have
// handed the text over, so it must never be retryable.
func TestDeliveryNeverStartedSet(t *testing.T) {
	retryable := []string{
		ReasonNoRunner, ReasonNoTmux, ReasonEnded, ReasonNoResumeTemplate,
		ReasonInteractiveNotAllowed, ReasonCwdOutsideProject,
		NotDeliveredPrefix + "queued", NotDeliveredPrefix + "paused_for_update",
		HandedOffPrefix + "job-1",
		InjectFailedPrefix + injectPaneMissing,
		InjectFailedPrefix + injectPaneBusyPrefix + "vim",
	}
	for _, reason := range retryable {
		assert.True(t, deliveryNeverStarted(reason), reason)
	}
	notRetryable := []string{
		DeliverFailedPrefix + "exit code 5: boom", // the command ran
		InjectFailedPrefix + injectRunnerError,    // it died mid-script
		"", ReasonNotRunning,
	}
	for _, reason := range notRetryable {
		assert.False(t, deliveryNeverStarted(reason), reason)
	}
}

// TestDeliverTmuxPendingKeepsThePath: the same 「送达中」 answer on path A, so the web
// shows which channel is carrying the text.
func TestDeliverTmuxPendingKeepsThePath(t *testing.T) {
	s := newSvc(t)
	inj := &pendingInjector{}
	s.SetInjector(inj)
	s.SetInjectProbe(inj)
	tmuxSession(t, s, "sid-inject-pending")
	_, err := s.Heartbeat("sid-inject-pending", HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)

	res, err := s.Deliver(context.Background(), "sid-inject-pending", "carry on", "alice", "alice", false)
	assert.NoErr(t, err)
	assert.True(t, res.Delivering)
	assert.Eq(t, PathTmux, res.Path)
	assert.Eq(t, "job-pending", res.JobID)
	assert.True(t, strings.Contains(strings.Join(inj.reqs[0].Tags, ","), TagRelayInject))
	// A tmux injection is gofer's own short control-plane work too: an update waiting for
	// idle must not park it.
	assert.True(t, inj.reqs[0].UpdateExempt, "a tmux injection must not be parked by an update")
}
