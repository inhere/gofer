package jobstore

import (
	"database/sql"
	"path/filepath"
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
	assert.Eq(t, RelayModeAuto, a.RelayMode)
	assert.Eq(t, int64(0), a.TurnNo)
	assert.True(t, a.StartedAt > 0)
	assert.Eq(t, a.StartedAt, a.LastSeenAt)

	// Re-register: non-empty fields overwrite, empty ones keep the stored value,
	// the relay switch / turn stay untouched.
	ok, err := s.SetSessionRelayMode("sid-1", RelayModeOn)
	assert.NoErr(t, err)
	assert.True(t, ok)
	a2, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-1", Agent: "claude", Title: "repo: fix bug"})
	assert.NoErr(t, err)
	assert.Eq(t, "p1", a2.ProjectKey)
	assert.Eq(t, "/work/repo", a2.Cwd)
	assert.Eq(t, "repo: fix bug", a2.Title)
	assert.Eq(t, RelayModeOn, a2.RelayMode)

	// Heartbeat: state + last_message; a non-empty title (sent only on a human prompt) replaces the old one.
	a3, ok, err := s.TouchAgentSession("sid-1", SessionHeartbeat{
		Event: "UserPromptSubmit", State: SessionIdle, LastMessage: "done, what next?", Title: "repo: now the tests",
	})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, SessionIdle, a3.State)
	assert.Eq(t, "UserPromptSubmit", a3.LastEvent)
	assert.Eq(t, "done, what next?", a3.LastMessage)
	assert.Eq(t, "repo: now the tests", a3.Title)

	// A heartbeat without a title (Stop / Notification) leaves the title alone.
	a3b, _, err := s.TouchAgentSession("sid-1", SessionHeartbeat{Event: "Stop", State: SessionIdle})
	assert.NoErr(t, err)
	assert.Eq(t, "repo: now the tests", a3b.Title)

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
	_, hasReleased := cols["released_by"]
	_, hasDetail := cols["detail"]
	assert.True(t, hasSid)
	assert.True(t, hasKind)
	assert.True(t, hasReleased)
	assert.True(t, hasDetail)
	old, ok, err := s.GetDecision("dec-old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", old.SessionID)
	assert.Eq(t, "", old.ReleasedBy)
	assert.Eq(t, "", old.Detail)
	// Idempotent.
	assert.NoErr(t, s.migrate())
}

// TestSessionIdleSecReported verifies the idle-detection column (SR-A5): a fresh
// session has never reported (unknown), a beat that carries a reading stores it,
// and a beat that carries none (OpenTurn's own touch, a plain event) leaves the
// stored reading alone — the relay keys its auto-arm on exactly this value.
func TestSessionIdleSecReported(t *testing.T) {
	s := openTest(t)
	secs := func(v int64) *int64 { return &v }

	a, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-i", Agent: "claude"})
	assert.NoErr(t, err)
	assert.Eq(t, int64(-1), a.IdleSec)

	a, ok, err := s.TouchAgentSession("sid-i", SessionHeartbeat{Event: "Stop", IdleSec: secs(600)})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(600), a.IdleSec)

	// No reading in the beat → the stored one survives (never silently cleared).
	a, ok, err = s.TouchAgentSession("sid-i", SessionHeartbeat{Event: "Stop", State: SessionWaitingReply})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(600), a.IdleSec)

	// An explicit 0 (the human just typed) is a real reading and does overwrite.
	a, ok, err = s.TouchAgentSession("sid-i", SessionHeartbeat{Event: "UserPromptSubmit", IdleSec: secs(0)})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(0), a.IdleSec)
}

// TestReleaseDecisionTagsExpiredTurn verifies the "released without an answer"
// path (SR-A5): only an OPEN turn can be released, it ends EXPIRED with the
// release tag instead of an answer, and a second release is a no-op.
func TestReleaseDecisionTagsExpiredTurn(t *testing.T) {
	s := openTest(t)
	d := PlanDecision{Title: "s · turn 1", Question: "what next?", TimeoutSec: 300, SessionID: "sid-r"}
	assert.NoErr(t, s.InsertDecision(&d))

	ok, err := s.ReleaseDecision(d.ID, "user_returned")
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, ok, err := s.GetDecision(d.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, DecisionExpired, got.State)
	assert.Eq(t, "user_returned", got.ReleasedBy)
	assert.Eq(t, "", got.Answer)

	ok, err = s.ReleaseDecision(d.ID, "user_returned") // already closed
	assert.NoErr(t, err)
	assert.False(t, ok)
	ok, err = s.ReleaseDecision("dec-missing", "user_returned")
	assert.NoErr(t, err)
	assert.False(t, ok)
	_, err = s.ReleaseDecision("", "user_returned")
	assert.Err(t, err)
}

// TestMigrateAgentSessionsAddsIdleSec opens a db whose agent_sessions predates
// the idle column: Open must add it, and a row written by the old binary reads
// back as "unknown" (-1) rather than as "the human is right there". The same
// migration adds the R1/R2 columns, whose defaults read as "nobody flipped the
// switch" (auto) and "no human seen yet" (0).
func TestMigrateAgentSessionsAddsIdleSec(t *testing.T) {
	s := openTest(t)
	for _, q := range []string{
		`DROP TABLE agent_sessions`,
		`CREATE TABLE agent_sessions (session_id TEXT PRIMARY KEY, agent TEXT NOT NULL,
  project_key TEXT, runner TEXT, cwd TEXT, title TEXT, transcript TEXT, tmux_pane TEXT,
  state TEXT NOT NULL DEFAULT 'running', relay INTEGER NOT NULL DEFAULT 0,
  turn_no INTEGER NOT NULL DEFAULT 0, last_message TEXT, last_event TEXT,
  last_seen_at INTEGER NOT NULL, started_at INTEGER NOT NULL, ended_at INTEGER)`,
		`INSERT INTO agent_sessions (session_id, agent, last_seen_at, started_at) VALUES ('sid-old','claude',1,1)`,
		// A row the pre-R1 binary left with the switch flipped on: the backfill must
		// read the historic `relay` column (kept in the DDL, unread everywhere else).
		`INSERT INTO agent_sessions (session_id, agent, relay, last_seen_at, started_at) VALUES ('sid-flipped','claude',1,1,1)`,
	} {
		_, err := s.db.Exec(q)
		assert.NoErr(t, err)
	}
	assert.NoErr(t, s.migrate())
	a, ok, err := s.GetAgentSession("sid-old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(-1), a.IdleSec)
	assert.Eq(t, RelayModeAuto, a.RelayMode)
	assert.Eq(t, int64(0), a.LastHumanAt)
	flipped, ok, err := s.GetAgentSession("sid-flipped")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, RelayModeOn, flipped.RelayMode)
	// P2-2: the takeover columns land with the same migration and a pre-column row
	// reads as "never taken over" — not as a session held by an empty job.
	assert.Eq(t, "", a.HandedOffJobID)
	assert.Eq(t, int64(0), a.HandedOffAt)
	assert.NoErr(t, s.migrate()) // idempotent

	// And the migrated table carries the takeover: a handoff round-trips, and the
	// release clears both columns plus the state.
	handed, err := s.SetSessionHandedOff("sid-old", "job-1")
	assert.NoErr(t, err)
	assert.Eq(t, SessionHandedOff, handed.State)
	assert.Eq(t, "job-1", handed.HandedOffJobID)
	assert.True(t, handed.HandedOffAt > 0)

	released, err := s.ReleaseSessionHandedOff("sid-old")
	assert.NoErr(t, err)
	assert.True(t, released)
	a, ok, err = s.GetAgentSession("sid-old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, SessionIdle, a.State)
	assert.Eq(t, "", a.HandedOffJobID)
	assert.Eq(t, int64(0), a.HandedOffAt)
	if _, err := s.SetSessionHandedOff("sid-missing", "job-1"); err == nil {
		t.Fatal("handing off an unknown session must be an error, not a silent no-op")
	}
}

// TestAgentSessionCallerIDRoundTrip: the AUTHENTICATED caller of a registration
// (bd h-aii-esus: the relay needs an owner to compare a reply against) is stored
// and read back on every session path — get, list and a later heartbeat. A
// re-registration without one keeps the stored value (a hook retrying mid-session
// must not blank the owner), and a session registered where no caller exists
// reads back as "" — the "old session" the owner check deliberately lets through.
func TestAgentSessionCallerIDRoundTrip(t *testing.T) {
	s := openTest(t)

	a, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-c", Agent: "claude", CallerID: "alice"})
	assert.NoErr(t, err)
	assert.Eq(t, "alice", a.CallerID)

	got, ok, err := s.GetAgentSession("sid-c")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "alice", got.CallerID)

	a2, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-c", Agent: "claude", Title: "repo: x"})
	assert.NoErr(t, err)
	assert.Eq(t, "alice", a2.CallerID)

	a3, ok, err := s.TouchAgentSession("sid-c", SessionHeartbeat{Event: "Stop", State: SessionIdle})
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "alice", a3.CallerID)

	// A session registered with no caller keeps "" — the migration-era row shape.
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "sid-anon", Agent: "codex"})
	assert.NoErr(t, err)
	anon, _, err := s.GetAgentSession("sid-anon")
	assert.NoErr(t, err)
	assert.Eq(t, "", anon.CallerID)

	list, err := s.ListAgentSessions(ListSessionsOpts{})
	assert.NoErr(t, err)
	byID := map[string]AgentSession{}
	for _, x := range list {
		byID[x.SessionID] = x
	}
	assert.Eq(t, "alice", byID["sid-c"].CallerID)
	assert.Eq(t, "", byID["sid-anon"].CallerID)
}

// TestAgentSessionCallerIDMigration opens a database written before the column
// existed and checks the additive migration: the row survives, reads back with an
// empty caller (the pre-column truth), and the column is writable afterwards.
func TestAgentSessionCallerIDMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-caller.db")
	db, err := sql.Open("sqlite", path)
	assert.NoErr(t, err)
	_, err = db.Exec(`CREATE TABLE agent_sessions (
  session_id TEXT PRIMARY KEY, agent TEXT NOT NULL, project_key TEXT, runner TEXT, cwd TEXT,
  title TEXT, transcript TEXT, tmux_pane TEXT, state TEXT NOT NULL DEFAULT 'running',
  relay_mode TEXT NOT NULL DEFAULT 'auto', relay INTEGER NOT NULL DEFAULT 0, idle_sec INTEGER,
  last_human_at INTEGER NOT NULL DEFAULT 0, turn_no INTEGER NOT NULL DEFAULT 0,
  last_message TEXT, last_event TEXT, last_seen_at INTEGER NOT NULL, started_at INTEGER NOT NULL,
  ended_at INTEGER)`)
	assert.NoErr(t, err)
	_, err = db.Exec(`INSERT INTO agent_sessions (session_id, agent, last_seen_at, started_at)
  VALUES ('sid-pre','claude',1,1)`)
	assert.NoErr(t, err)
	assert.NoErr(t, db.Close())

	st, err := Open(path)
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	got, ok, err := st.GetAgentSession("sid-pre")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", got.CallerID)

	stamped, err := st.UpsertAgentSession(AgentSession{SessionID: "sid-pre", CallerID: "bob"})
	assert.NoErr(t, err)
	assert.Eq(t, "bob", stamped.CallerID)
}
