package today

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestPermissionCard(t *testing.T) {
	svc, st := newTestService(t)
	_, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sid-1", Agent: "claude", ProjectKey: "p1", Title: "repo: 清理依赖"})
	assert.NoErr(t, err)
	d := jobstore.PlanDecision{Title: "repo · 需要授权", Question: "需要授权：Bash `rm -rf node_modules`", SessionID: "sid-1",
		Kind: jobstore.DecisionKindPermission, TimeoutSec: 600, AskedAt: ago(1),
		Detail: `{"tool_name":"Bash","summary":"Bash ` + "`rm -rf node_modules`" + `","suggestions":[{"label":"规则 Bash(rm:*)"}],"fp":"x"}`}
	assert.NoErr(t, st.InsertDecision(&d))

	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	assert.Eq(t, []string{"permission:" + d.ID}, keys(cards))
	c := cards[0]
	assert.Eq(t, "需要授权", c.Tag)
	assert.Eq(t, "repo: 清理依赖", c.Title)
	assert.Eq(t, "Bash `rm -rf node_modules`", c.Summary)
	assert.Eq(t, "sid-1", c.Refs.SessionID)
	assert.Eq(t, "r:sid-1", c.Refs.ThreadID)
	assert.Eq(t, []string{"answer", "answer", "answer", "deny_note"}, actionIDs(c.Actions))
	assert.Eq(t, "allow", c.Actions[0].Value)
	assert.Eq(t, "always:0", c.Actions[1].Value)
	assert.Eq(t, "总是允许：规则 Bash(rm:*)", c.Actions[1].Label)
	assert.Eq(t, "deny", c.Actions[2].Value)
	assert.True(t, c.Actions[3].NeedsText)
	alive, err := svc.cardAlive(c.Key)
	assert.NoErr(t, err)
	assert.True(t, alive)

	// settled anywhere → gone
	_, err = st.ReleaseDecision(d.ID, "terminal")
	assert.NoErr(t, err)
	cards, err = svc.Decisions(false)
	assert.NoErr(t, err)
	assert.Len(t, cards, 0)
	alive, err = svc.cardAlive(c.Key)
	assert.NoErr(t, err)
	assert.False(t, alive)
}
