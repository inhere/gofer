package workbench

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/waitgate"
)

const gateNow = int64(1_000_000)

func relayAt(askedAgo int64, needs bool) []jobstore.PlanDecision {
	return []jobstore.PlanDecision{{ID: "d1", SessionID: "s1", AskedAt: gateNow - askedAgo, NeedsReply: needs}}
}

func relaySession() jobstore.AgentSession {
	return jobstore.AgentSession{SessionID: "s1", Agent: "claude", ProjectKey: "p", State: jobstore.SessionWaitingReply, LastSeenAt: gateNow - 10}
}

// A terminal turn waits on a person only after the grace period, like the 「等我」 list.
func TestRelayThreadAttentionWaitsOutTheGracePeriod(t *testing.T) {
	young := projectRelayThread("r:s1", relaySession(), jobstore.WorkbenchThreadPref{}, relayAt(waitgate.GraceSec-1, true), nil, gateNow)
	assert.True(t, young.attention == nil)
	assert.Eq(t, StatusIdle, young.thread.Status)

	old := projectRelayThread("r:s1", relaySession(), jobstore.WorkbenchThreadPref{}, relayAt(waitgate.GraceSec, true), nil, gateNow)
	assert.True(t, old.attention != nil)
	assert.Eq(t, StatusBlocked, old.thread.Status)
	got := markAttention([]projection{old})
	assert.True(t, got[0].thread.Attention)
}

func acpJob(status string) jobstore.JobRecord {
	return jobstore.JobRecord{ID: "acp1", ProjectKey: "p", Agent: "claude-acp", SessionID: "sid-1", Status: status,
		SessionStateJSON: `{"session":true,"turn_no":1}`}
}

func TestACPThreadAttentionNeedsMarkerAndGrace(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		turns   map[string]jobstore.AwaitingTurn
		wantAtt bool
	}{
		{"marked and past grace", "awaiting_input", map[string]jobstore.AwaitingTurn{"acp1": {JobID: "acp1", At: gateNow - waitgate.GraceSec}}, true},
		{"marked inside grace", "awaiting_input", map[string]jobstore.AwaitingTurn{"acp1": {JobID: "acp1", At: gateNow - 10}}, false},
		{"not marked", "awaiting_input", nil, false},
		{"marker but the person already answered (running)", "running", map[string]jobstore.AwaitingTurn{"acp1": {JobID: "acp1", At: gateNow - 3600}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := jobstore.WorkbenchSnapshot{Jobs: []jobstore.JobRecord{acpJob(c.status)}, AwaitingTurns: c.turns}
			got := projectThreads(snap, 0, nil, gateNow)
			assert.Len(t, got, 1)
			assert.Eq(t, c.wantAtt, got[0].attention != nil)
			assert.Eq(t, c.wantAtt, got[0].thread.Attention)
			if c.status == "awaiting_input" {
				assert.Eq(t, StatusAwaitingInput, got[0].thread.Status)
			}
		})
	}
}
