package sessionrelay

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func subBeat(t *testing.T, s *Service, sid, event, id string) jobstore.AgentSession {
	t.Helper()
	delta := 1
	if event == EventSubagentStop {
		delta = -1
	}
	a, err := s.Heartbeat(sid, HeartbeatInput{Event: event, SubagentID: id, SubagentDelta: delta})
	assert.NoErr(t, err)
	return a
}

func TestSubagentCountDedupeAndExpiry(t *testing.T) {
	s := newSvc(t)
	now := time.Now()
	s.nowFn = func() time.Time { return now }
	_, err := s.Register(RegisterInput{SessionID: "sa", Agent: "claude", Event: EventSessionStart})
	assert.NoErr(t, err)

	subBeat(t, s, "sa", EventSubagentStart, "a1")
	subBeat(t, s, "sa", EventSubagentStart, "a1") // duplicate start: same agent
	subBeat(t, s, "sa", EventSubagentStart, "a2")
	assert.Eq(t, 2, s.SubagentCount("sa"))
	subBeat(t, s, "sa", EventSubagentStop, "a1")
	subBeat(t, s, "sa", EventSubagentStop, "a1") // duplicate stop is a no-op
	assert.Eq(t, 1, s.SubagentCount("sa"))

	// a lost SubagentStop expires after 2h without events
	now = now.Add(SubagentExpiry + time.Minute)
	assert.Eq(t, 0, s.SubagentCount("sa"))

	// the sub-agent beat is bookkeeping: last_event stays the session's own event
	row, _, _ := s.store.GetAgentSession("sa")
	assert.Eq(t, EventSessionStart, row.LastEvent)

	// id-less events: start/stop still balance
	subBeat(t, s, "sa", EventSubagentStart, "")
	assert.Eq(t, 1, s.SubagentCount("sa"))
	subBeat(t, s, "sa", EventSubagentStop, "")
	assert.Eq(t, 0, s.SubagentCount("sa"))

	// SessionEnd clears
	subBeat(t, s, "sa", EventSubagentStart, "z")
	_, err = s.Heartbeat("sa", HeartbeatInput{Event: EventSessionEnd})
	assert.NoErr(t, err)
	assert.Eq(t, 0, s.SubagentCount("sa"))
}

func TestSubagentSupervisingSkipsAutoArm(t *testing.T) {
	s := newSvc(t)
	s.AutoArmIdleSec = 300
	s.SkipWhenSupervising, s.SupervisingWindowSec = true, 7200
	_, err := s.Register(RegisterInput{SessionID: "sb", Agent: "claude"})
	assert.NoErr(t, err)
	a := jobstore.AgentSession{SessionID: "sb", RelayMode: jobstore.RelayModeAuto, IdleSec: 600}

	assert.Eq(t, WaitIdleProbe, s.WaitReason(a))
	subBeat(t, s, "sb", EventSubagentStart, "a1")
	reason, detail := s.WaitDecision(a)
	assert.Eq(t, "", reason)
	assert.Eq(t, "supervising 1 subagents", detail)
	// explicit on still wins
	a.RelayMode = jobstore.RelayModeOn
	assert.Eq(t, WaitModeOn, s.WaitReason(a))
	a.RelayMode = jobstore.RelayModeAuto
	subBeat(t, s, "sb", EventSubagentStop, "a1")
	assert.Eq(t, WaitIdleProbe, s.WaitReason(a))

	// knob off: sub-agents do not gate
	s.SkipWhenSupervising = false
	subBeat(t, s, "sb", EventSubagentStart, "a2")
	assert.Eq(t, WaitIdleProbe, s.WaitReason(a))
}

func TestSubagentStopReleasesBlockedAutoTurn(t *testing.T) {
	s := newSvc(t)
	s.AutoArmIdleSec = 300
	_, err := s.Register(RegisterInput{SessionID: "sc", Agent: "claude"})
	assert.NoErr(t, err)
	// the Stop beat reports idle 600 -> armed
	idle := int64(600)
	_, err = s.Heartbeat("sc", HeartbeatInput{Event: EventStop, IdleSec: &idle})
	assert.NoErr(t, err)
	// two sub-agents were started before (still running while Stop blocks)
	s.subagents.apply("sc", "a1", 1, time.Now().Unix())
	s.subagents.apply("sc", "a2", 1, time.Now().Unix())
	s.SkipWhenSupervising = false // keep the wait armed so the turn can open
	d, err := s.OpenTurn("sc", "done?", 600)
	assert.NoErr(t, err)

	subBeat(t, s, "sc", EventSubagentStop, "a1")
	got, _, _ := s.store.GetDecision(d.ID)
	assert.Eq(t, jobstore.DecisionOpen, got.State, "one sub-agent still runs")

	subBeat(t, s, "sc", EventSubagentStop, "a2")
	got, _, _ = s.store.GetDecision(d.ID)
	assert.Eq(t, jobstore.DecisionExpired, got.State)
	assert.Eq(t, ReleaseBySubagentDone, got.ReleasedBy)
	row, _, _ := s.store.GetAgentSession("sc")
	assert.Eq(t, jobstore.SessionIdle, row.State)
}

func TestSubagentStopKeepsExplicitOnTurn(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sd", Agent: "claude"})
	assert.NoErr(t, err)
	_, err = s.SetRelayMode("sd", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	d, err := s.OpenTurn("sd", "q", 600)
	assert.NoErr(t, err)
	subBeat(t, s, "sd", EventSubagentStart, "a1")
	subBeat(t, s, "sd", EventSubagentStop, "a1")
	got, _, _ := s.store.GetDecision(d.ID)
	assert.Eq(t, jobstore.DecisionOpen, got.State)
}

func TestWaitBudgetSec(t *testing.T) {
	s := newSvc(t)
	assert.Eq(t, 0, s.WaitBudgetSec(WaitModeOn)) // unset service: no cap
	s.SetWaitBudgets(3600, 600)
	assert.Eq(t, 3600, s.WaitBudgetSec(WaitModeOn))
	assert.Eq(t, 600, s.WaitBudgetSec(WaitIdleProbe))
	assert.Eq(t, 600, s.WaitBudgetSec(WaitTurnAge))
	assert.Eq(t, 0, s.WaitBudgetSec(""))
	s.SetWaitBudgets(0, 0)
	assert.Eq(t, 0, s.WaitBudgetSec(WaitModeOn))
}
