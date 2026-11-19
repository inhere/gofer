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
	seedSourceSessionJob(t, s, "job-sv-1", "sv-j", "claude-sv", "self", "local", "", "running", now.Unix()-60)
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
	seedSourceSessionJob(t, s, "job-so-1", "sv-o", "claude-so", "self", "local", "", "running", now.Unix()-10)
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

// Only this session's own jobs count: a same-caller job from another session
// does not, a resident session job waiting for a person does not, and a job
// this session submitted does.
func TestSupervisionIsPerSession(t *testing.T) {
	s := newSvc(t)
	s.SupervisingWindowSec = 7200
	now := time.Now()
	s.nowFn = func() time.Time { return now }
	a := jobstore.AgentSession{SessionID: "sv-mine", CallerID: "default"}

	seedSourceSessionJob(t, s, "job-pj-other", "sv-other", "default", "self", "local", "", "running", now.Unix()-5)
	assert.False(t, s.Supervision(a).Any())

	seedSourceSessionJob(t, s, "job-pj-mine", "sv-mine", "default", "self", "local", "", "running", now.Unix()-5)
	assert.Eq(t, 1, s.Supervision(a).Jobs)
	finishJob(t, s, "job-pj-mine", "done")
	assert.False(t, s.Supervision(a).Any())

	// A resident session job parked in awaiting_input is waiting for a person.
	seedSourceSessionJob(t, s, "job-pj-sess", "sv-mine", "default", "self", "local", "", "awaiting_input", now.Unix()-5)
	rec, ok, err := s.store.GetJob("job-pj-sess")
	assert.NoErr(t, err)
	assert.True(t, ok)
	rec.SessionStateJSON = `{"turn":1}`
	assert.NoErr(t, s.store.UpsertJob(rec))
	assert.False(t, s.Supervision(a).Any())

	// A plain job awaiting input is still live work.
	seedSourceSessionJob(t, s, "job-pj-plain", "sv-mine", "default", "self", "local", "", "awaiting_input", now.Unix()-5)
	assert.Eq(t, 1, s.Supervision(a).Jobs)
}
