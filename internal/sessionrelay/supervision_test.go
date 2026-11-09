package sessionrelay

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestSupervisionCountsWatches(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sv-w", Agent: "claude"})
	assert.NoErr(t, err)
	a := jobstore.AgentSession{SessionID: "sv-w"}
	assert.False(t, s.Supervision(a).Any())

	_, err = s.store.AddSessionJobWatch("sv-w", "job-w1")
	assert.NoErr(t, err)
	v := s.Supervision(a)
	assert.True(t, v.Any())
	assert.Eq(t, 1, v.Watches)
	assert.Eq(t, 0, v.Jobs)
	assert.Eq(t, 0, v.Subagents)
}

func TestSupervisionCountsJobsAndSubagents(t *testing.T) {
	s := newSvc(t)
	s.SupervisingWindowSec = 7200
	now := time.Now()
	s.nowFn = func() time.Time { return now }
	_, err := s.Register(RegisterInput{SessionID: "sv-j", Agent: "claude"})
	assert.NoErr(t, err)
	a := jobstore.AgentSession{SessionID: "sv-j", CallerID: "claude-sv"}
	seedCallerJob(t, s, "job-sv-1", "claude-sv", "running", now.Unix()-60)
	subBeat(t, s, "sv-j", EventSubagentStart, "a1")
	v := s.Supervision(a)
	assert.Eq(t, Supervision{Jobs: 1, Subagents: 1}, v)
}

// The fact ignores the Stop gate's switches: knob off and relay_mode=off still report.
func TestSupervisionIgnoresSwitch(t *testing.T) {
	s := newSvc(t)
	s.SkipWhenSupervising, s.SupervisingWindowSec = false, 7200
	now := time.Now()
	s.nowFn = func() time.Time { return now }
	_, err := s.Register(RegisterInput{SessionID: "sv-o", Agent: "claude"})
	assert.NoErr(t, err)
	a := jobstore.AgentSession{SessionID: "sv-o", CallerID: "claude-so", RelayMode: jobstore.RelayModeOff}
	seedCallerJob(t, s, "job-so-1", "claude-so", "running", now.Unix()-10)
	assert.Eq(t, 1, s.Supervision(a).Jobs)
}

// Characterization: a watch alone does not change the Stop gate (WaitReason and
// the supervising detail stay as before the Supervision fact existed).
func TestSupervisingDetailIgnoresWatches(t *testing.T) {
	s := newSvc(t)
	s.AutoArmIdleSec = 300
	s.SkipWhenSupervising, s.SupervisingWindowSec = true, 7200
	_, err := s.Register(RegisterInput{SessionID: "sv-d", Agent: "claude"})
	assert.NoErr(t, err)
	a := jobstore.AgentSession{SessionID: "sv-d", RelayMode: jobstore.RelayModeAuto, CallerID: "claude-sd", IdleSec: 600}
	_, err = s.store.AddSessionJobWatch("sv-d", "job-sd-1")
	assert.NoErr(t, err)

	reason, detail := s.WaitDecision(a)
	assert.Eq(t, WaitIdleProbe, reason)
	assert.Eq(t, "", detail)
	d, ok := s.supervisingDetail(a)
	assert.False(t, ok)
	assert.Eq(t, "", d)
}
