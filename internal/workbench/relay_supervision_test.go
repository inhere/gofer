package workbench

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

func TestProjectRelayThreadSupervising(t *testing.T) {
	session := func(state string) jobstore.AgentSession {
		return jobstore.AgentSession{SessionID: "s1", Agent: "claude", ProjectKey: "p", State: state, LastSeenAt: 100}
	}
	turn := func(needsReply bool) []jobstore.PlanDecision {
		return []jobstore.PlanDecision{{ID: "d1", SessionID: "s1", AskedAt: 50, NeedsReply: needsReply}}
	}
	supervised := func(jobstore.AgentSession) sessionrelay.Supervision {
		return sessionrelay.Supervision{Jobs: 2, Watches: 1}
	}
	alone := func(jobstore.AgentSession) sessionrelay.Supervision { return sessionrelay.Supervision{} }

	t.Run("marked and supervised is working with counts", func(t *testing.T) {
		p := projectRelayThread("r:s1", session(jobstore.SessionWaitingReply), jobstore.WorkbenchThreadPref{}, turn(true), supervised)
		assert.Eq(t, StatusWorking, p.thread.Status)
		assert.True(t, p.attention == nil)
		assert.Eq(t, &Supervising{Jobs: 2, Watches: 1}, p.thread.Supervising)
	})
	t.Run("unmarked turn is idle", func(t *testing.T) {
		p := projectRelayThread("r:s1", session(jobstore.SessionWaitingReply), jobstore.WorkbenchThreadPref{}, turn(false), alone)
		assert.Eq(t, StatusIdle, p.thread.Status)
		assert.True(t, p.attention == nil)
	})
	t.Run("marked and alone stays blocked with attention", func(t *testing.T) {
		p := projectRelayThread("r:s1", session(jobstore.SessionWaitingReply), jobstore.WorkbenchThreadPref{}, turn(true), alone)
		assert.Eq(t, StatusBlocked, p.thread.Status)
		assert.True(t, p.attention != nil)
		assert.True(t, p.thread.Supervising == nil)
	})
	t.Run("no open turn but waiting_reply keeps the old behaviour", func(t *testing.T) {
		p := projectRelayThread("r:s1", session(jobstore.SessionWaitingReply), jobstore.WorkbenchThreadPref{}, nil, supervised)
		assert.Eq(t, StatusBlocked, p.thread.Status)
		assert.True(t, p.attention != nil)
	})
	t.Run("nil lookup counts as not supervised", func(t *testing.T) {
		p := projectRelayThread("r:s1", session(jobstore.SessionWaitingReply), jobstore.WorkbenchThreadPref{}, turn(true), nil)
		assert.Eq(t, StatusBlocked, p.thread.Status)
	})
}

// A relay without a Supervision method (test doubles) reads as nothing supervising.
func TestRelaySupervisionAssertion(t *testing.T) {
	assert.True(t, NewService(nil, nil, &recordingRelay{}).relaySupervision() == nil)
}
