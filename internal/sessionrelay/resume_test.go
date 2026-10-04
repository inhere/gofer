package sessionrelay

import (
	"context"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func endTakeoverSession(t *testing.T, s *Service, sid string) {
	t.Helper()
	_, err := s.Heartbeat(sid, HeartbeatInput{Event: EventSessionEnd})
	assert.NoErr(t, err)
	a, err := s.Session(sid)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionEnded, a.State)
}

// A closed terminal is what Resume exists for: deliverTakeover no longer refuses
// ended sessions, and a plain wake-up types nothing.
func TestResumeEndedSessionStartsTakeover(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-wake-1"}}
	s.SetTakeoverer(to)
	takeoverSession(t, s, "sid-wake-0001")
	endTakeoverSession(t, s, "sid-wake-0001")

	res, err := s.Resume(context.Background(), "sid-wake-0001", "", "alice")
	assert.NoErr(t, err)
	assert.Eq(t, PathTakeover, res.Path)
	assert.Len(t, to.reqs, 1)
	assert.Eq(t, "", to.reqs[0].InitialInput)
	assert.Eq(t, "claude --resume sid-wake-0001", strings.Join(to.reqs[0].Cmd, " "))
	a, _ := s.Session("sid-wake-0001")
	assert.Eq(t, jobstore.SessionHandedOff, a.State)
	assert.Eq(t, "job-wake-1", a.HandedOffJobID)

	// The audit row exists and carries a placeholder for the missing first message.
	list, err := s.store.ListSessionDecisions("sid-wake-0001", "", 10, "")
	assert.NoErr(t, err)
	assert.Len(t, list.Decisions, 1)

	// The takeover job ending hands the session back as ended, not idle.
	released, err := s.ReleaseTakeoverForJob(context.Background(), "job-wake-1")
	assert.NoErr(t, err)
	assert.True(t, released)
	a, _ = s.Session("sid-wake-0001")
	assert.Eq(t, jobstore.SessionEnded, a.State)
	assert.Eq(t, "", a.HandedOffJobID)
}

// Deliver (typing a reply) still refuses an ended session: only the explicit wake-up
// path starts a process for it.
func TestDeliverStillRefusesEndedSession(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-x"}}
	s.SetTakeoverer(to)
	takeoverSession(t, s, "sid-wake-0002")
	endTakeoverSession(t, s, "sid-wake-0002")
	_, err := s.Deliver(context.Background(), "sid-wake-0002", "hi", "alice", true)
	assert.Eq(t, ReasonEnded, DeliverReason(err))
	assert.Len(t, to.reqs, 0)
}

func TestPlanResumeReasonsAreChinese(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{}
	s.SetTakeoverer(to)
	takeoverSession(t, s, "sid-plan-0001")

	to.planFn = func() TakeoverPlan { return TakeoverPlan{ExecRoot: defaultTakeoverRoot} }
	p, err := s.PlanResume("sid-plan-0001")
	assert.NoErr(t, err)
	assert.False(t, p.Can)
	assert.Eq(t, ReasonNoResumeTemplate, p.Reason)
	assert.StrContains(t, p.Message, "session_resume_interactive")

	to.planFn = nil
	p, err = s.PlanResume("sid-plan-0001")
	assert.NoErr(t, err)
	assert.True(t, p.Can)
	assert.Eq(t, "sub", p.Cwd)
	assert.Eq(t, "w-claude", p.Runner)
	assert.StrContains(t, p.Message, "w-claude")
	assert.NotEq(t, "", p.Warning, "a session not marked ended may still have its terminal open")
	assert.Len(t, to.reqs, 0, "planning must not start anything")

	_, err = s.PlanResume("nope")
	assert.Err(t, err)
}

func TestResumeFailureWrapsChineseExplanation(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{planFn: func() TakeoverPlan {
		return TakeoverPlan{Argv: []string{"claude"}, AllowInteractive: false, ExecRoot: defaultTakeoverRoot}
	}}
	s.SetTakeoverer(to)
	takeoverSession(t, s, "sid-plan-0002")
	_, err := s.Resume(context.Background(), "sid-plan-0002", "", "alice")
	assert.Eq(t, ReasonInteractiveNotAllowed, DeliverReason(err))
	assert.StrContains(t, err.Error(), "allow_interactive")
	assert.StrContains(t, err.Error(), "交互终端")
}

// An offline session (silent too long, never ended) can be woken like an ended one,
// and the plan says its original process may be gone.
func TestResumeOfflineSessionAllowed(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-off-1"}}
	s.SetTakeoverer(to)
	takeoverSession(t, s, "sid-off-0001")
	ok, err := s.store.MarkStaleSessionsOffline(1 << 40)
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), ok)
	a, _ := s.Session("sid-off-0001")
	assert.Eq(t, jobstore.SessionOffline, a.State)

	p, err := s.PlanResume("sid-off-0001")
	assert.NoErr(t, err)
	assert.True(t, p.Can)
	assert.StrContains(t, p.Message, "离线")
	assert.StrContains(t, p.Warning, "可能已退出")

	_, err = s.Resume(context.Background(), "sid-off-0001", "", "alice")
	assert.NoErr(t, err)
	assert.Len(t, to.reqs, 1)
}
