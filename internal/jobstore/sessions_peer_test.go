package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// The session peer address, store half (design 2026-11-05, plan T1): the column, its
// three-state heartbeat update, and the rule that one address belongs to one row per
// runner (the pid-reuse case). The rows that need a hand-made runner label are built
// with plain SQL, because every API normalises the label (G043).

const testPeerSocket = "/run/user/1000/cc-socks/4242.sock"

func peerSession(id, runner, socket string) AgentSession {
	return AgentSession{
		SessionID:     id,
		Agent:         "claude",
		Runner:        runner,
		PeerName:      "proj-4d",
		PeerMessaging: true,
		PeerSocket:    socket,
	}
}

func peerStored(t *testing.T, s *Store, id string) AgentSession {
	t.Helper()
	a, ok, err := s.GetAgentSession(id)
	assert.NoErr(t, err)
	assert.True(t, ok, "session %s exists", id)
	return a
}

func TestUpsertAgentSessionPeerSocket(t *testing.T) {
	t.Run("stored on insert", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-a", "local", testPeerSocket))
		assert.NoErr(t, err)
		assert.Eq(t, testPeerSocket, peerStored(t, s, "sid-a").PeerSocket)
	})

	t.Run("a report without peer news keeps the stored address", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-a", "local", testPeerSocket))
		assert.NoErr(t, err)
		// No name, no status, PeerMessaging false: the peer block is not taken, so
		// neither the address nor anybody else's address is touched.
		_, err = s.UpsertAgentSession(AgentSession{SessionID: "sid-a", Agent: "claude", Title: "renamed"})
		assert.NoErr(t, err)
		assert.Eq(t, testPeerSocket, peerStored(t, s, "sid-a").PeerSocket)
	})

	t.Run("an empty address in the peer block clears it", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-a", "local", testPeerSocket))
		assert.NoErr(t, err)
		_, err = s.UpsertAgentSession(peerSession("sid-a", "local", ""))
		assert.NoErr(t, err)
		assert.Eq(t, "", peerStored(t, s, "sid-a").PeerSocket)
	})
}

func TestUpsertAgentSessionClearsPeerSocketOfSameRunner(t *testing.T) {
	t.Run("the old row of the same runner loses the address", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-old", "local", testPeerSocket))
		assert.NoErr(t, err)
		// A new process re-uses the dead one's pid, hence its address.
		_, err = s.UpsertAgentSession(peerSession("sid-new", "local", testPeerSocket))
		assert.NoErr(t, err)
		assert.Eq(t, testPeerSocket, peerStored(t, s, "sid-new").PeerSocket)
		old := peerStored(t, s, "sid-old")
		assert.Eq(t, "", old.PeerSocket, "the old row lost the address")
		assert.False(t, old.PeerMessaging, "the old row is not deliverable by name either")
	})

	t.Run("the other spelling of the built-in runner counts as the same runner", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-old", "local", testPeerSocket))
		assert.NoErr(t, err)
		// Stored verbatim, the way an old row may have written it (G043).
		_, err = s.db.Exec(`UPDATE agent_sessions SET runner='server' WHERE session_id=?`, "sid-old")
		assert.NoErr(t, err)
		_, err = s.UpsertAgentSession(peerSession("sid-new", "local", testPeerSocket))
		assert.NoErr(t, err)
		assert.Eq(t, "", peerStored(t, s, "sid-old").PeerSocket)
	})

	t.Run("another runner keeps its address", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-other", "w-lab", testPeerSocket))
		assert.NoErr(t, err)
		_, err = s.UpsertAgentSession(peerSession("sid-new", "local", testPeerSocket))
		assert.NoErr(t, err)
		other := peerStored(t, s, "sid-other")
		assert.Eq(t, testPeerSocket, other.PeerSocket, "a different runner is a different address space")
		assert.True(t, other.PeerMessaging)
	})

	t.Run("a row without a runner neither takes nor gives an address", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-other", "local", testPeerSocket))
		assert.NoErr(t, err)
		// A client-mode container reports no runner at all (plan R2).
		_, err = s.UpsertAgentSession(peerSession("sid-new", "", testPeerSocket))
		assert.NoErr(t, err)
		assert.Eq(t, testPeerSocket, peerStored(t, s, "sid-other").PeerSocket,
			"an unlabelled row must not strip a runner-labelled row")
	})

	t.Run("a re-registration with an empty runner clears by the STORED runner", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-old", "local", testPeerSocket))
		assert.NoErr(t, err)
		_, err = s.UpsertAgentSession(peerSession("sid-new", "local", testPeerSocket))
		assert.NoErr(t, err)
		// Hand the address back to the old row, then re-register the new one
		// without a runner: its row keeps `local`, so the clean-up must still run.
		_, err = s.db.Exec(`UPDATE agent_sessions SET peer_socket=?, peer_messaging=1 WHERE session_id=?`,
			testPeerSocket, "sid-old")
		assert.NoErr(t, err)
		_, err = s.UpsertAgentSession(peerSession("sid-new", "", testPeerSocket))
		assert.NoErr(t, err)
		assert.Eq(t, "local", peerStored(t, s, "sid-new").Runner, "the stored runner is kept")
		assert.Eq(t, "", peerStored(t, s, "sid-old").PeerSocket)
	})
}

func TestTouchAgentSessionPeerSocket(t *testing.T) {
	socket := testPeerSocket

	t.Run("nil leaves the stored address alone", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-a", "local", socket))
		assert.NoErr(t, err)
		_, ok, err := s.TouchAgentSession("sid-a", SessionHeartbeat{Event: "Stop"})
		assert.NoErr(t, err)
		assert.True(t, ok)
		assert.Eq(t, socket, peerStored(t, s, "sid-a").PeerSocket)
	})

	t.Run("an empty address clears it", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-a", "local", socket))
		assert.NoErr(t, err)
		empty := ""
		_, ok, err := s.TouchAgentSession("sid-a", SessionHeartbeat{Event: "Stop", PeerSocket: &empty})
		assert.NoErr(t, err)
		assert.True(t, ok)
		assert.Eq(t, "", peerStored(t, s, "sid-a").PeerSocket)
	})

	t.Run("a beat clears the old row of the stored runner", func(t *testing.T) {
		s := openTest(t)
		// The old row spells the built-in runner the other way round.
		_, err := s.UpsertAgentSession(peerSession("sid-old", "local", ""))
		assert.NoErr(t, err)
		_, err = s.db.Exec(`UPDATE agent_sessions SET runner='server', peer_socket=?, peer_messaging=1
  WHERE session_id=?`, socket, "sid-old")
		assert.NoErr(t, err)
		// A beat carries no runner: the clean-up must use the row's stored label.
		_, err = s.UpsertAgentSession(AgentSession{SessionID: "sid-new", Agent: "claude", Runner: "server"})
		assert.NoErr(t, err)
		_, ok, err := s.TouchAgentSession("sid-new", SessionHeartbeat{Event: "Notification", PeerSocket: &socket})
		assert.NoErr(t, err)
		assert.True(t, ok)
		assert.Eq(t, socket, peerStored(t, s, "sid-new").PeerSocket)
		old := peerStored(t, s, "sid-old")
		assert.Eq(t, "", old.PeerSocket)
		assert.False(t, old.PeerMessaging)
	})

	t.Run("an unknown session changes nothing and is not an error", func(t *testing.T) {
		s := openTest(t)
		_, ok, err := s.TouchAgentSession("sid-ghost", SessionHeartbeat{Event: "Stop", PeerSocket: &socket})
		assert.NoErr(t, err, "the hook registers first and retries, so this stays a plain ok=false")
		assert.False(t, ok)
	})

	t.Run("a failing clean-up rolls the whole beat back", func(t *testing.T) {
		s := openTest(t)
		_, err := s.UpsertAgentSession(peerSession("sid-old", "local", socket))
		assert.NoErr(t, err)
		_, err = s.UpsertAgentSession(AgentSession{SessionID: "sid-new", Agent: "claude", Runner: "local"})
		assert.NoErr(t, err)
		// Make the clean-up of the old row fail: the beat's own write must not land
		// half-applied (the transaction is one unit).
		_, err = s.db.Exec(`CREATE TRIGGER peer_boom BEFORE UPDATE ON agent_sessions
  WHEN OLD.session_id='sid-old' BEGIN SELECT RAISE(ABORT, 'boom'); END`)
		assert.NoErr(t, err)
		_, ok, err := s.TouchAgentSession("sid-new", SessionHeartbeat{Event: "Stop", PeerSocket: &socket})
		assert.Err(t, err)
		assert.False(t, ok)
		assert.Eq(t, "", peerStored(t, s, "sid-new").PeerSocket, "the beat was rolled back")
		assert.Eq(t, socket, peerStored(t, s, "sid-old").PeerSocket)
	})
}

func TestListLiveSessionsByPeerName(t *testing.T) {
	s := openTest(t)
	_, err := s.UpsertAgentSession(peerSession("sid-a", "local", testPeerSocket))
	assert.NoErr(t, err)
	_, err = s.UpsertAgentSession(peerSession("sid-b", "w-lab", ""))
	assert.NoErr(t, err)
	_, err = s.UpsertAgentSession(peerSession("sid-ended", "local", ""))
	assert.NoErr(t, err)
	_, _, err = s.TouchAgentSession("sid-ended", SessionHeartbeat{State: SessionEnded})
	assert.NoErr(t, err)
	// A session with another name is not a duplicate.
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "sid-x", Agent: "claude", PeerName: "other"})
	assert.NoErr(t, err)

	got, err := s.ListLiveSessionsByPeerName("proj-4d")
	assert.NoErr(t, err)
	ids := make([]string, 0, len(got))
	for _, a := range got {
		ids = append(ids, a.SessionID)
	}
	assert.Eq(t, []string{"sid-a", "sid-b"}, ids, "ended and other-named sessions are not duplicates")

	empty, err := s.ListLiveSessionsByPeerName(" ")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(empty), "no name means no lookup")
}

func TestListLiveSessionsByPeerSocket(t *testing.T) {
	s := openTest(t)
	// A stale row can still hold the same address until its next heartbeat: both
	// rows are answers, and the caller decides which one (if any) it belongs to.
	// They are planted with SQL because a real report would clear the other row.
	_, err := s.UpsertAgentSession(peerSession("sid-a", "local", testPeerSocket))
	assert.NoErr(t, err)
	for _, id := range []string{"sid-stale", "sid-ended"} {
		_, err = s.UpsertAgentSession(AgentSession{SessionID: id, Agent: "claude", Runner: "local"})
		assert.NoErr(t, err)
		_, err = s.db.Exec(`UPDATE agent_sessions SET peer_socket=?, peer_messaging=1 WHERE session_id=?`,
			testPeerSocket, id)
		assert.NoErr(t, err)
	}
	_, err = s.db.Exec(`UPDATE agent_sessions SET state='ended', ended_at=1 WHERE session_id=?`, "sid-ended")
	assert.NoErr(t, err)

	got, err := s.ListLiveSessionsByPeerSocket(testPeerSocket)
	assert.NoErr(t, err)
	ids := make([]string, 0, len(got))
	for _, a := range got {
		ids = append(ids, a.SessionID)
	}
	assert.Eq(t, []string{"sid-a", "sid-stale"}, ids)

	empty, err := s.ListLiveSessionsByPeerSocket("")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(empty), "no address means no lookup")
}
