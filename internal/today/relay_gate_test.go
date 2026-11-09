package today

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

type fakeSupervisor struct{ v sessionrelay.Supervision }

func (f *fakeSupervisor) Supervision(jobstore.AgentSession) sessionrelay.Supervision { return f.v }

// gateFixture seeds one session with one relay turn asked askedAgo seconds ago.
func gateFixture(t *testing.T, needsReply bool, askedAgo int64) (*Service, *fakeSupervisor) {
	t.Helper()
	svc, st := newTestService(t)
	sup := &fakeSupervisor{}
	svc.d.Supervisor = sup
	_, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "g1", Agent: "claude", ProjectKey: "p1", Title: "t"})
	assert.NoErr(t, err)
	assert.NoErr(t, st.InsertDecision(&jobstore.PlanDecision{ID: "gd", Title: "turn", Question: "q", Kind: jobstore.DecisionKindRelay,
		SessionID: "g1", AskedAt: testNow.Unix() - askedAgo, TimeoutSec: 86400, NeedsReply: needsReply}))
	return svc, sup
}

func TestRelayGateSupervisedExcluded(t *testing.T) {
	for name, v := range map[string]sessionrelay.Supervision{
		"jobs": {Jobs: 1}, "subagents": {Subagents: 1}, "watches": {Watches: 1},
	} {
		t.Run(name, func(t *testing.T) {
			svc, sup := gateFixture(t, true, 600)
			sup.v = v
			cards, err := svc.Decisions(false)
			assert.NoErr(t, err)
			assert.Eq(t, 0, len(cards))
		})
	}
}

func TestRelayGateSupervisionStartsAfterOpen(t *testing.T) {
	svc, sup := gateFixture(t, true, 600)
	cards, _ := svc.Decisions(false)
	assert.Eq(t, []string{"relay:g1"}, keys(cards))
	sup.v = sessionrelay.Supervision{Watches: 1} // supervision starts after the turn opened
	cards, _ = svc.Decisions(false)
	assert.Eq(t, 0, len(cards))
	sup.v = sessionrelay.Supervision{} // and ends again
	cards, _ = svc.Decisions(false)
	assert.Eq(t, []string{"relay:g1"}, keys(cards))
}

func TestRelayGateGrace(t *testing.T) {
	// 299s old: held back, recheck_at = asked_at + 300.
	svc, _ := gateFixture(t, true, 299)
	cards, recheck, err := svc.decisionsIn("", false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(cards))
	assert.Eq(t, testNow.Unix()-299+relayGraceSec, recheck)
	resp, err := svc.Today(Query{})
	assert.NoErr(t, err)
	assert.Eq(t, recheck, resp.RecheckAt)

	// 300s old: in, nothing to recheck.
	svc, _ = gateFixture(t, true, 300)
	cards, recheck, _ = svc.decisionsIn("", false)
	assert.Eq(t, []string{"relay:g1"}, keys(cards))
	assert.Eq(t, int64(0), recheck)
}

func TestRelayGateRecheckIgnoresSupervisedAndUnmarked(t *testing.T) {
	svc, sup := gateFixture(t, true, 10)
	sup.v = sessionrelay.Supervision{Jobs: 1}
	_, recheck, _ := svc.decisionsIn("", false)
	assert.Eq(t, int64(0), recheck, "a supervised turn is not waiting on the grace period")

	svc, _ = gateFixture(t, false, 10)
	_, recheck, _ = svc.decisionsIn("", false)
	assert.Eq(t, int64(0), recheck, "an unmarked turn never qualifies")
}

func TestRelayGateNeedsReplyOnly(t *testing.T) {
	svc, _ := gateFixture(t, false, 3600)
	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(cards))
}
