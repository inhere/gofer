package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	_ "modernc.org/sqlite"
)

// gofer-eh53, store half: the refusal note (who was refused, when), the dedicated
// owner hand-over, and the additive migration that gives both to an existing db.

func TestClaimAgentSessionOwnerRecordsRefusal(t *testing.T) {
	s := openTest(t)
	_, _, err := s.UpsertAgentSessionForOwner(AgentSession{SessionID: "sid-own", Agent: "claude", CallerID: "alice"})
	assert.NoErr(t, err)

	// A foreign beat is refused AND noted.
	a, claimed, err := s.ClaimAgentSessionOwner("sid-own", "bob")
	assert.NoErr(t, err)
	assert.False(t, claimed, "a foreign caller never claims")
	assert.Eq(t, "alice", a.CallerID, "ownership does not move")
	assert.Eq(t, "bob", a.RefusedCallerID)
	assert.True(t, a.RefusedAt > 0)

	// A caller-less beat claims nothing and is not blamed (the note names a caller).
	if _, claimed, err := s.ClaimAgentSessionOwner("sid-own", ""); err != nil || claimed {
		t.Fatalf("caller-less claim: claimed=%v err=%v", claimed, err)
	}
	stored, _, err := s.GetAgentSession("sid-own")
	assert.NoErr(t, err)
	assert.Eq(t, "bob", stored.RefusedCallerID, "the earlier note stays")

	if _, claimed, err = s.ClaimAgentSessionOwner("sid-own", "alice"); err != nil || !claimed {
		t.Fatalf("owner claim: claimed=%v err=%v", claimed, err)
	}

	// The owner's own beat clears the note (on the row itself).
	a, ok, err := s.GetAgentSession("sid-own")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", a.RefusedCallerID)
	assert.Eq(t, int64(0), a.RefusedAt)
}

func TestUpsertAgentSessionForOwnerRecordsRefusal(t *testing.T) {
	s := openTest(t)
	_, _, err := s.UpsertAgentSessionForOwner(AgentSession{SessionID: "sid-reg", Agent: "claude", CallerID: "alice"})
	assert.NoErr(t, err)

	existing, allowed, err := s.UpsertAgentSessionForOwner(AgentSession{SessionID: "sid-reg", Agent: "claude", CallerID: "bob", Title: "hi"})
	assert.NoErr(t, err)
	assert.False(t, allowed, "a foreign registration is refused")
	assert.Eq(t, "bob", existing.RefusedCallerID)
	assert.Eq(t, "alice", existing.CallerID)

	// The owner's own re-registration clears it.
	_, allowed, err = s.UpsertAgentSessionForOwner(AgentSession{SessionID: "sid-reg", Agent: "claude", CallerID: "alice"})
	assert.NoErr(t, err)
	assert.True(t, allowed)
	a, _, err := s.GetAgentSession("sid-reg")
	assert.NoErr(t, err)
	assert.Eq(t, "", a.RefusedCallerID)
	assert.Eq(t, int64(0), a.RefusedAt)
}

func TestTransferAgentSessionOwner(t *testing.T) {
	s := openTest(t)
	_, _, err := s.UpsertAgentSessionForOwner(AgentSession{SessionID: "sid-x", Agent: "claude", ProjectKey: "self", CallerID: "alice"})
	assert.NoErr(t, err)
	if _, claimed, err := s.ClaimAgentSessionOwner("sid-x", "bob"); err != nil || claimed {
		t.Fatalf("setup: bob must not claim alice's session (claimed=%v err=%v)", claimed, err)
	}

	// Unknown session: nothing to hand over.
	if _, _, ok, err := s.TransferAgentSessionOwner("sid-none", "bob", "root"); err != nil || ok {
		t.Fatalf("transfer of an unknown session: ok=%v err=%v", ok, err)
	}

	a, from, ok, err := s.TransferAgentSessionOwner("sid-x", "bob", "root")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "alice", from)
	assert.Eq(t, "bob", a.CallerID)
	assert.Eq(t, "", a.RefusedCallerID, "the note described the ownership that just ended")
	assert.Eq(t, int64(0), a.RefusedAt)
	assert.Eq(t, "self", a.ProjectKey, "everything else about the session stays")

	// ...and the row really holds it (not just the returned copy).
	stored, _, err := s.GetAgentSession("sid-x")
	assert.NoErr(t, err)
	assert.Eq(t, "bob", stored.CallerID)
	assert.Eq(t, "", stored.RefusedCallerID)

	// The other direction is now the refused one.
	_, claimed, err := s.ClaimAgentSessionOwner("sid-x", "alice")
	assert.NoErr(t, err)
	assert.False(t, claimed)
	back, _, err := s.GetAgentSession("sid-x")
	assert.NoErr(t, err)
	assert.Eq(t, "alice", back.RefusedCallerID)
}

// TestRefusedCallerMigrationFromLegacyDB: a pre-gofer-eh53 db (no refused columns)
// opens, migrates, and reads back "never refused" — the additive path an existing
// server takes on upgrade.
func TestRefusedCallerMigrationFromLegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-refused.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE agent_sessions (
  session_id TEXT PRIMARY KEY, agent TEXT NOT NULL, project_key TEXT, runner TEXT, cwd TEXT,
  title TEXT, transcript TEXT, tmux_pane TEXT, state TEXT NOT NULL DEFAULT 'running',
  relay INTEGER NOT NULL DEFAULT 0, idle_sec INTEGER, turn_no INTEGER NOT NULL DEFAULT 0,
  last_message TEXT, last_event TEXT, last_seen_at INTEGER NOT NULL, started_at INTEGER NOT NULL,
  ended_at INTEGER)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO agent_sessions (session_id, agent, last_seen_at, started_at)
  VALUES ('sid-legacy','claude',1,1)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.True(t, tableHasColumn(t, s, "agent_sessions", "refused_caller_id"))
	assert.True(t, tableHasColumn(t, s, "agent_sessions", "refused_at"))
	a, ok, err := s.GetAgentSession("sid-legacy")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", a.RefusedCallerID, "a pre-column row was never refused")
	assert.Eq(t, int64(0), a.RefusedAt)

	// The columns exist and take a note on that very row.
	_, _, err = s.UpsertAgentSessionForOwner(AgentSession{SessionID: "sid-legacy", Agent: "claude", CallerID: "alice"})
	assert.NoErr(t, err)
	got, claimed, err := s.ClaimAgentSessionOwner("sid-legacy", "bob")
	assert.NoErr(t, err)
	assert.False(t, claimed)
	assert.Eq(t, "bob", got.RefusedCallerID)
}
