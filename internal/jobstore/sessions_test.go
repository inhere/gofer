package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestAgentSessionUpsertTouchList(t *testing.T) {
	s := openTest(t)

	a, err := s.UpsertAgentSession(AgentSession{
		SessionID: "sid-1", Agent: "claude", ProjectKey: "p1", Runner: "server",
		Cwd: "/work/repo", Transcript: "/home/u/.claude/projects/x/sid-1.jsonl", LastEvent: "SessionStart",
	})
	assert.NoErr(t, err)
	assert.Eq(t, SessionRunning, a.State)
	assert.False(t, a.Relay)
	assert.Eq(t, int64(0), a.TurnNo)
	assert.True(t, a.StartedAt > 0)
	assert.Eq(t, a.StartedAt, a.LastSeenAt)

	// Re-register: non-empty fields overwrite, empty ones keep the stored value,
	// relay/turn untouched.
	ok, err := s.SetSessionRelay("sid-1", true)
	assert.NoErr(t, err)
	assert.True(t, ok)
	a2, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-1", Agent: "claude", Title: "repo: fix bug"})
	assert.NoErr(t, err)
	assert.Eq(t, "p1", a2.ProjectKey)
	assert.Eq(t, "/work/repo", a2.Cwd)
	assert.Eq(t, "repo: fix bug", a2.Title)
	assert.True(t, a2.Relay)

	// Heartbeat: state + last_message; title only fills an empty title.
	a3, ok, err := s.TouchAgentSession("sid-1", SessionHeartbeat{
		Event: "Stop", State: SessionIdle, LastMessage: "done, what next?", Title: "ignored",
	})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, SessionIdle, a3.State)
	assert.Eq(t, "Stop", a3.LastEvent)
	assert.Eq(t, "done, what next?", a3.LastMessage)
	assert.Eq(t, "repo: fix bug", a3.Title)

	_, ok, err = s.TouchAgentSession("sid-nope", SessionHeartbeat{Event: "Stop"})
	assert.NoErr(t, err)
	assert.False(t, ok)

	// Second session in another cwd, waiting_reply sorts first.
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "sid-2", Agent: "codex", ProjectKey: "p1", Cwd: "/work/other"})
	assert.NoErr(t, err)
	ok, err = s.SetSessionState("sid-2", SessionWaitingReply)
	assert.NoErr(t, err)
	assert.True(t, ok)

	list, err := s.ListAgentSessions(ListSessionsOpts{Project: "p1"})
	assert.NoErr(t, err)
	assert.Len(t, list, 2)
	assert.Eq(t, "sid-2", list[0].SessionID)

	byAgent, err := s.ListAgentSessions(ListSessionsOpts{Agent: "codex"})
	assert.NoErr(t, err)
	assert.Len(t, byAgent, 1)

	// Cwd match: exact and descendant, not sibling.
	byCwd, err := s.ListAgentSessions(ListSessionsOpts{Cwd: "/work/repo/sub/dir"})
	assert.NoErr(t, err)
	assert.Len(t, byCwd, 1)
	assert.Eq(t, "sid-1", byCwd[0].SessionID)
	byCwd, err = s.ListAgentSessions(ListSessionsOpts{Cwd: "/work/repo2"})
	assert.NoErr(t, err)
	assert.Len(t, byCwd, 0)

	// Turn counter.
	turn, ok, err := s.IncrSessionTurn("sid-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(1), turn)
	turn, _, _ = s.IncrSessionTurn("sid-1")
	assert.Eq(t, int64(2), turn)
	_, ok, err = s.IncrSessionTurn("sid-nope")
	assert.NoErr(t, err)
	assert.False(t, ok)

	// Ended sessions drop out of the default list, come back with IncludeEnded,
	// and a re-register revives them.
	_, ok, err = s.TouchAgentSession("sid-2", SessionHeartbeat{Event: "SessionEnd", State: SessionEnded})
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _, _ := s.GetAgentSession("sid-2")
	assert.True(t, got.EndedAt > 0)
	list, _ = s.ListAgentSessions(ListSessionsOpts{})
	assert.Len(t, list, 1)
	list, _ = s.ListAgentSessions(ListSessionsOpts{IncludeEnded: true})
	assert.Len(t, list, 2)
	revived, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-2", Agent: "codex"})
	assert.NoErr(t, err)
	assert.Eq(t, SessionRunning, revived.State)
	assert.Eq(t, int64(0), revived.EndedAt)

	// Delete.
	ok, err = s.DeleteAgentSession("sid-2")
	assert.NoErr(t, err)
	assert.True(t, ok)
	_, ok, _ = s.GetAgentSession("sid-2")
	assert.False(t, ok)

	// Validation.
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "", Agent: "claude"})
	assert.Err(t, err)
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "x", Agent: "claude", State: "weird"})
	assert.Err(t, err)
	_, err = s.ListAgentSessions(ListSessionsOpts{State: "weird"})
	assert.Err(t, err)
}

func TestRelayDecisionsPerSession(t *testing.T) {
	s := openTest(t)
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-r", Agent: "claude"})
	assert.NoErr(t, err)

	now := time.Now().Unix()
	d1 := PlanDecision{Title: "turn 1", Question: "first stop", SessionID: "sid-r", Kind: DecisionKindRelay, TimeoutSec: 60, AskedAt: now - 3}
	assert.NoErr(t, s.InsertDecision(&d1))
	d2 := PlanDecision{Title: "turn 2", Question: "second stop", SessionID: "sid-r", Kind: DecisionKindRelay, TimeoutSec: 60, AskedAt: now - 2}
	assert.NoErr(t, s.InsertDecision(&d2))
	plain := PlanDecision{Title: "ask", Question: "plain ask_human", TimeoutSec: 60, AskedAt: now - 1}
	assert.NoErr(t, s.InsertDecision(&plain))

	got, ok, err := s.GetDecision(d1.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "sid-r", got.SessionID)
	assert.Eq(t, DecisionKindRelay, got.Kind)
	p, _, _ := s.GetDecision(plain.ID)
	assert.Eq(t, "", p.SessionID)
	assert.Eq(t, "", p.Kind)

	// Newest first, only this session's rows.
	list, err := s.ListSessionDecisions("sid-r", "", 0)
	assert.NoErr(t, err)
	assert.Len(t, list, 2)
	assert.Eq(t, d2.ID, list[0].ID)

	// answered + list by state
	ok, err = s.AnswerDecision(d1.ID, "go on", "human")
	assert.NoErr(t, err)
	assert.True(t, ok)
	openList, err := s.ListSessionDecisions("sid-r", DecisionOpen, 0)
	assert.NoErr(t, err)
	assert.Len(t, openList, 1)
	assert.Eq(t, d2.ID, openList[0].ID)

	// relay off → open turns expire; plain decision untouched.
	n, err := s.ExpireSessionDecisions("sid-r")
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), n)
	e, _, _ := s.GetDecision(d2.ID)
	assert.Eq(t, DecisionExpired, e.State)
	p, _, _ = s.GetDecision(plain.ID)
	assert.Eq(t, DecisionOpen, p.State)

	_, err = s.ListSessionDecisions("", "", 0)
	assert.Err(t, err)
}

// TestMigratePlanDecisionsAdditive opens a db whose plan_decisions predates the
// session-relay columns and checks Open adds them (old rows read back as "").
func TestMigratePlanDecisionsAdditive(t *testing.T) {
	s := openTest(t)
	// Simulate a pre-SESS-01 table: drop the two columns by recreating the table.
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_plan_decisions_session`,
		`DROP TABLE plan_decisions`,
		`CREATE TABLE plan_decisions (id TEXT PRIMARY KEY, plan_id TEXT, title TEXT NOT NULL,
  question TEXT NOT NULL, options_json TEXT, answer TEXT, state TEXT NOT NULL DEFAULT 'OPEN',
  timeout_sec INTEGER NOT NULL DEFAULT 1800, asked_at INTEGER NOT NULL, answered_at INTEGER, answered_by TEXT)`,
		`INSERT INTO plan_decisions (id, title, question, asked_at) VALUES ('dec-old','t','q', 1)`,
	} {
		_, err := s.db.Exec(q)
		assert.NoErr(t, err)
	}
	assert.NoErr(t, s.migrate())
	cols, err := s.tableColumns("plan_decisions")
	assert.NoErr(t, err)
	_, hasSid := cols["session_id"]
	_, hasKind := cols["kind"]
	assert.True(t, hasSid)
	assert.True(t, hasKind)
	old, ok, err := s.GetDecision("dec-old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", old.SessionID)
	// Idempotent.
	assert.NoErr(t, s.migrate())
}
