package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/runner"
)

func TestSessionNudgeLifecycleAndCleanup(t *testing.T) {
	s := openTest(t)
	cur := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return cur })
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "sn1", Agent: "claude"})
	assert.NoErr(t, err)

	_, err = s.CreateSessionNudge(SessionNudge{SessionID: "ghost", Kind: NudgeEvery, IntervalSec: 60, Text: "x"})
	assert.Err(t, err)
	n, err := s.CreateSessionNudge(SessionNudge{SessionID: "sn1", Kind: NudgeEvery, IntervalSec: 600, Text: " hi ", CreatedBy: "alice"})
	assert.NoErr(t, err)
	assert.Eq(t, "hi", n.Text)
	assert.Eq(t, cur.Unix()+600, n.NextRunAt)

	// stalled nudges carry no timer; their clock is last_fired_at / created_at
	st, err := s.CreateSessionNudge(SessionNudge{SessionID: "sn1", Kind: NudgeStalled, IntervalSec: 600, Text: "x"})
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), st.NextRunAt)

	// consecutive failures pause at the limit; a stale attempt on a paused nudge is ignored
	for i := 1; i <= 3; i++ {
		got, paused, err := s.RecordNudgeAttempt(n.ID, cur.Unix()+int64(i), false, "boom", cur.Unix()+1000, 3)
		assert.NoErr(t, err)
		assert.Eq(t, i == 3, paused)
		assert.Eq(t, int64(i), got.FailCount)
	}
	got, _, err := s.RecordNudgeAttempt(n.ID, cur.Unix()+9, true, "", 0, 3)
	assert.NoErr(t, err)
	assert.Eq(t, NudgePaused, got.State)
	assert.Eq(t, int64(3), got.FailCount)

	ok, err := s.ResumeSessionNudge(n.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _, _ = s.GetSessionNudge(n.ID)
	assert.Eq(t, NudgeActive, got.State)
	assert.Eq(t, int64(0), got.FailCount)

	act, err := s.ListActiveNudges()
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(act))
	ok, err = s.EndSessionNudge(st.ID, NudgeEndedUntil)
	assert.NoErr(t, err)
	assert.True(t, ok)
	ok, _ = s.EndSessionNudge(st.ID, NudgeEndedUntil)
	assert.False(t, ok)
	list, _ := s.ListSessionNudges("sn1", false)
	assert.Eq(t, 1, len(list))
	list, _ = s.ListSessionNudges("sn1", true)
	assert.Eq(t, 2, len(list))

	_, err = s.DeleteAgentSession("sn1")
	assert.NoErr(t, err)
	list, _ = s.ListSessionNudges("", true)
	assert.Eq(t, 0, len(list))
}

func TestAddSessionUsageStampsUsageAt(t *testing.T) {
	s := openTest(t)
	cur := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return cur })
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "su-at", Agent: "claude"})
	assert.NoErr(t, err)
	a, _, _ := s.GetAgentSession("su-at")
	assert.Eq(t, int64(0), a.UsageAt)
	cur = cur.Add(time.Minute)
	_, err = s.AddSessionUsage("su-at", runnerUsage(1))
	assert.NoErr(t, err)
	a, _, _ = s.GetAgentSession("su-at")
	assert.Eq(t, cur.Unix(), a.UsageAt)
}

func runnerUsage(n int64) runner.SessionUsage { return runner.SessionUsage{Main: usageOf(n, 0)} }
