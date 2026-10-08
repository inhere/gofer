package sessionrelay

import (
	"context"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
)

type nudgeNotifyFake struct {
	fakeNotifier
	paused []string
}

func (f *nudgeNotifyFake) NotifySessionNudgePaused(sid, _, _, nudgeID, reason string) {
	f.paused = append(f.paused, sid+":"+nudgeID+":"+reason)
}

// nudgeSession registers a messenger-reachable session (resident messenger on the
// server's own runner) in the given state and returns the messenger fake.
func nudgeSession(t *testing.T, s *Service, sid, event string) *residentFake {
	t.Helper()
	f := &residentFake{}
	s.SetMessenger(f)
	_, err := s.Register(RegisterInput{SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: "local",
		PeerName: "peer-" + sid, PeerMessaging: true, Event: EventSessionStart})
	assert.NoErr(t, err)
	if event != "" {
		_, err = s.Heartbeat(sid, HeartbeatInput{Event: event})
		assert.NoErr(t, err)
	}
	return f
}

func mkNudge(t *testing.T, s *Service, sid, kind string, every time.Duration) jobstore.SessionNudge {
	t.Helper()
	n, err := s.CreateNudge(sid, NudgeInput{Kind: kind, Interval: every, Text: "ping", By: "alice"})
	assert.NoErr(t, err)
	return n
}

func TestNudgeEveryFiresWhenDueOnly(t *testing.T) {
	s := newSvc(t)
	f := nudgeSession(t, s, "sid-ng-every", EventStop)
	n := mkNudge(t, s, "sid-ng-every", jobstore.NudgeEvery, 30*time.Minute)
	now := time.Now().Unix()

	assert.Eq(t, 0, s.SweepNudges(context.Background(), now+60))
	assert.Eq(t, 0, len(f.residentCalls))

	assert.Eq(t, 1, s.SweepNudges(context.Background(), now+30*60+1))
	assert.Eq(t, 1, len(f.residentCalls))
	got, err := s.Nudge(n.ID)
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), got.FireCount)
	assert.Eq(t, now+30*60+1+30*60, got.NextRunAt)

	// not due again right away
	assert.Eq(t, 0, s.SweepNudges(context.Background(), now+30*60+10))
}

func TestNudgeStalledRespectsProgress(t *testing.T) {
	s := newSvc(t)
	t0 := time.Now()
	clock := t0
	s.store.SetClock(func() time.Time { return clock })
	f := nudgeSession(t, s, "sid-ng-stall", EventUserPromptSubmit) // running
	n := mkNudge(t, s, "sid-ng-stall", jobstore.NudgeStalled, 20*time.Minute)

	// 15 min in: a usage growth counts as progress.
	clock = t0.Add(15 * time.Minute)
	ok, err := s.store.AddSessionUsage("sid-ng-stall", runner.SessionUsage{Main: runner.Usage{InputTokens: 5, TotalTokens: 5}})
	assert.NoErr(t, err)
	assert.True(t, ok)

	// 25 min: only 10 min since the usage grew -> no nudge, although 25 > 20 since start.
	assert.Eq(t, 0, s.SweepNudges(context.Background(), t0.Add(25*time.Minute).Unix()))
	assert.Eq(t, 0, len(f.residentCalls))

	// 36 min: 21 min of silence -> nudged once.
	assert.Eq(t, 1, s.SweepNudges(context.Background(), t0.Add(36*time.Minute).Unix()))
	assert.Eq(t, 1, len(f.residentCalls))

	// the same stall is not nudged again before another interval passes
	assert.Eq(t, 0, s.SweepNudges(context.Background(), t0.Add(50*time.Minute).Unix()))
	assert.Eq(t, 1, s.SweepNudges(context.Background(), t0.Add(57*time.Minute).Unix()))
	got, _ := s.Nudge(n.ID)
	assert.Eq(t, int64(2), got.FireCount)
}

func TestNudgeStalledStateRules(t *testing.T) {
	s := newSvc(t)
	t0 := time.Now()
	f := nudgeSession(t, s, "sid-ng-idle", EventStop) // idle
	mkNudge(t, s, "sid-ng-idle", jobstore.NudgeStalled, 10*time.Minute)
	late := t0.Add(time.Hour).Unix()

	// idle without an open work item: not stalled, just finished.
	assert.Eq(t, 0, s.SweepNudges(context.Background(), late))
	assert.Eq(t, 0, len(f.residentCalls))

	// idle WITH an unfinished work item: stalled.
	_, err := s.store.CreateWorkItem(jobstore.WorkItemInput{Title: "ship it", SessionIDs: []string{"sid-ng-idle"}})
	assert.NoErr(t, err)
	assert.Eq(t, 1, s.SweepNudges(context.Background(), late))

	// a done work item no longer counts
	s2 := newSvc(t)
	f2 := nudgeSession(t, s2, "sid-ng-done", EventStop)
	mkNudge(t, s2, "sid-ng-done", jobstore.NudgeStalled, 10*time.Minute)
	w, err := s2.store.CreateWorkItem(jobstore.WorkItemInput{Title: "x", Status: jobstore.WorkDone, SessionIDs: []string{"sid-ng-done"}})
	assert.NoErr(t, err)
	_ = w
	assert.Eq(t, 0, s2.SweepNudges(context.Background(), late))
	assert.Eq(t, 0, len(f2.residentCalls))
}

func TestNudgePausesAfterThreeFailuresAndNotifies(t *testing.T) {
	s := newSvc(t)
	nf := &nudgeNotifyFake{}
	s.SetNotifier(nf)
	// no messenger wired: every delivery fails.
	_, err := s.Register(RegisterInput{SessionID: "sid-ng-fail", Agent: "claude", ProjectKey: "self", Runner: "local",
		PeerName: "p", PeerMessaging: true, Event: EventSessionStart})
	assert.NoErr(t, err)
	n := mkNudge(t, s, "sid-ng-fail", jobstore.NudgeEvery, time.Minute)
	now := time.Now().Unix()
	for i := 1; i <= 3; i++ {
		assert.Eq(t, 0, s.SweepNudges(context.Background(), now+int64(i)*61))
		got, _ := s.Nudge(n.ID)
		assert.Eq(t, int64(i), got.FailCount)
		if i < 3 {
			assert.Eq(t, jobstore.NudgeActive, got.State)
			assert.Eq(t, 0, len(nf.paused))
		}
	}
	got, _ := s.Nudge(n.ID)
	assert.Eq(t, jobstore.NudgePaused, got.State)
	assert.Eq(t, 1, len(nf.paused))
	// a paused nudge is not swept any more
	assert.Eq(t, 0, s.SweepNudges(context.Background(), now+10_000))
	got, _ = s.Nudge(n.ID)
	assert.Eq(t, int64(3), got.FailCount)

	// resume clears the failure count and re-arms the timer
	got, err = s.ResumeNudge(n.ID)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.NudgeActive, got.State)
	assert.Eq(t, int64(0), got.FailCount)
}

func TestNudgeSuccessResetsFailureCount(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sid-ng-reset", Agent: "claude", ProjectKey: "self", Runner: "local",
		PeerName: "p", PeerMessaging: true, Event: EventSessionStart})
	assert.NoErr(t, err)
	n := mkNudge(t, s, "sid-ng-reset", jobstore.NudgeEvery, time.Minute)
	now := time.Now().Unix()
	s.SweepNudges(context.Background(), now+61) // fails (no messenger)
	s.SweepNudges(context.Background(), now+130)
	got, _ := s.Nudge(n.ID)
	assert.Eq(t, int64(2), got.FailCount)
	f := &residentFake{}
	s.SetMessenger(f)
	_, err = s.Heartbeat("sid-ng-reset", HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
	assert.Eq(t, 1, s.SweepNudges(context.Background(), now+200))
	got, _ = s.Nudge(n.ID)
	assert.Eq(t, int64(0), got.FailCount)
	assert.Eq(t, jobstore.NudgeActive, got.State)
}

func TestNudgeEndsWithSessionAndUntil(t *testing.T) {
	s := newSvc(t)
	nudgeSession(t, s, "sid-ng-end", EventStop)
	n := mkNudge(t, s, "sid-ng-end", jobstore.NudgeEvery, time.Minute)
	_, err := s.Heartbeat("sid-ng-end", HeartbeatInput{Event: EventSessionEnd})
	assert.NoErr(t, err)
	assert.Eq(t, 0, s.SweepNudges(context.Background(), time.Now().Unix()+3600))
	got, _ := s.Nudge(n.ID)
	assert.Eq(t, jobstore.NudgeEnded, got.State)
	assert.Eq(t, jobstore.NudgeEndedSessionEnd, got.EndedReason)
	// an ended session takes no new nudge
	_, err = s.CreateNudge("sid-ng-end", NudgeInput{Kind: jobstore.NudgeEvery, Interval: time.Minute, Text: "x"})
	assert.ErrIs(t, err, ErrInvalidInput)

	// until
	f := nudgeSession(t, s, "sid-ng-until", EventStop)
	now := time.Now().Unix()
	n2, err := s.CreateNudge("sid-ng-until", NudgeInput{Kind: jobstore.NudgeEvery, Interval: time.Minute, Text: "x", UntilAt: now + 100})
	assert.NoErr(t, err)
	assert.Eq(t, 0, s.SweepNudges(context.Background(), now+200))
	assert.Eq(t, 0, len(f.residentCalls))
	got, _ = s.Nudge(n2.ID)
	assert.Eq(t, jobstore.NudgeEndedUntil, got.EndedReason)

	// handed off
	nudgeSession(t, s, "sid-ng-ho", EventStop)
	n3 := mkNudge(t, s, "sid-ng-ho", jobstore.NudgeEvery, time.Minute)
	_, err = s.store.SetSessionHandedOff("sid-ng-ho", "job-1")
	assert.NoErr(t, err)
	s.SweepNudges(context.Background(), now+500)
	got, _ = s.Nudge(n3.ID)
	assert.Eq(t, jobstore.NudgeEndedHandedOff, got.EndedReason)
}

func TestNudgeValidationAndManage(t *testing.T) {
	s := newSvc(t)
	nudgeSession(t, s, "sid-ng-val", EventStop)
	for _, in := range []NudgeInput{
		{Kind: "weekly", Interval: time.Hour, Text: "x"},
		{Kind: jobstore.NudgeEvery, Interval: 10 * time.Second, Text: "x"},
		{Kind: jobstore.NudgeEvery, Interval: time.Hour, Text: "  "},
		{Kind: jobstore.NudgeEvery, Interval: time.Hour, Text: "x", UntilAt: 5},
	} {
		_, err := s.CreateNudge("sid-ng-val", in)
		assert.ErrIs(t, err, ErrInvalidInput)
	}
	_, err := s.CreateNudge("nope", NudgeInput{Kind: jobstore.NudgeEvery, Interval: time.Hour, Text: "x"})
	assert.ErrIs(t, err, ErrUnknownSession)

	n := mkNudge(t, s, "sid-ng-val", jobstore.NudgeEvery, time.Hour)
	_, err = s.ResumeNudge(n.ID)
	assert.ErrIs(t, err, ErrInvalidInput)
	got, err := s.PauseNudge(n.ID)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.NudgePaused, got.State)
	list, err := s.Nudges("sid-ng-val", false)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(list))
	assert.NoErr(t, s.RemoveNudge(n.ID))
	assert.ErrIs(t, s.RemoveNudge(n.ID), ErrNudgeNotFound)
}
