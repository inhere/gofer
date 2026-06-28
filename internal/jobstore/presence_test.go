package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// samplePresence builds a PresenceRecord for tests.
func samplePresence(agentID, name string, lastSeen int64) PresenceRecord {
	return PresenceRecord{
		AgentID:      agentID,
		AgentToken:   "tok-" + agentID,
		Name:         name,
		Role:         "reviewer",
		ProjectKey:   "demo",
		CallerID:     "caller-1",
		Client:       "1.2.3.4",
		Status:       "online",
		RegisteredAt: lastSeen,
		LastSeenAt:   lastSeen,
		MetaJSON:     `{"k":"v"}`,
	}
}

func TestUpsertPresenceRoundTrip(t *testing.T) {
	s := openTest(t)

	in := samplePresence("a1", "alice", 1000)
	assert.NoErr(t, s.UpsertPresence(in))

	got, ok, err := s.GetPresence("a1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "a1", got.AgentID)
	assert.Eq(t, "tok-a1", got.AgentToken)
	assert.Eq(t, "alice", got.Name)
	assert.Eq(t, "reviewer", got.Role)
	assert.Eq(t, "demo", got.ProjectKey)
	assert.Eq(t, "caller-1", got.CallerID)
	assert.Eq(t, "1.2.3.4", got.Client)
	assert.Eq(t, "online", got.Status)
	assert.Eq(t, int64(1000), got.RegisteredAt)
	assert.Eq(t, int64(1000), got.LastSeenAt)
	assert.Eq(t, `{"k":"v"}`, got.MetaJSON)
}

func TestGetPresenceNotFound(t *testing.T) {
	s := openTest(t)
	_, ok, err := s.GetPresence("nope")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// TestUpsertPresenceIsRegisterThenRenew proves re-registering the same agent_id
// is an update on ONE row (续约 keeps a single latest row, refreshing last_seen).
func TestUpsertPresenceIsRegisterThenRenew(t *testing.T) {
	s := openTest(t)

	first := samplePresence("a1", "alice", 500)
	assert.NoErr(t, s.UpsertPresence(first))

	renew := first
	renew.LastSeenAt = 900
	renew.Status = "online"
	assert.NoErr(t, s.UpsertPresence(renew))

	list, err := s.ListPresence()
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, int64(900), list[0].LastSeenAt)
	assert.Eq(t, int64(500), list[0].RegisteredAt) // registered_at not overwritten
}

func TestListPresenceOrderedByLastSeenDesc(t *testing.T) {
	s := openTest(t)

	assert.NoErr(t, s.UpsertPresence(samplePresence("a", "alice", 100)))
	assert.NoErr(t, s.UpsertPresence(samplePresence("c", "carol", 300)))
	assert.NoErr(t, s.UpsertPresence(samplePresence("b", "bob", 200)))

	list, err := s.ListPresence()
	assert.NoErr(t, err)
	assert.Len(t, list, 3)
	assert.Eq(t, "c", list[0].AgentID) // last_seen 300
	assert.Eq(t, "b", list[1].AgentID) // 200
	assert.Eq(t, "a", list[2].AgentID) // 100
}

func TestListPresenceEmpty(t *testing.T) {
	s := openTest(t)
	list, err := s.ListPresence()
	assert.NoErr(t, err)
	assert.Len(t, list, 0)
}

func TestTouchPresenceRefreshesLastSeen(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertPresence(samplePresence("a1", "alice", 1000)))

	assert.NoErr(t, s.TouchPresence("a1", 2000))
	got, ok, err := s.GetPresence("a1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(2000), got.LastSeenAt)

	// Touching an absent agent is a no-op, not an error.
	assert.NoErr(t, s.TouchPresence("ghost", 3000))
}

func TestDeletePresenceIdempotent(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertPresence(samplePresence("a1", "alice", 1000)))

	assert.NoErr(t, s.DeletePresence("a1"))
	_, ok, err := s.GetPresence("a1")
	assert.NoErr(t, err)
	assert.False(t, ok)

	// Deleting again is a no-op.
	assert.NoErr(t, s.DeletePresence("a1"))
}

func TestPrunePresenceDropsStale(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertPresence(samplePresence("old", "alice", 100)))
	assert.NoErr(t, s.UpsertPresence(samplePresence("fresh", "bob", 500)))

	// cutoff=300: only "old" (last_seen 100 < 300) is pruned.
	n, err := s.PrunePresence(300)
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)

	list, err := s.ListPresence()
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, "fresh", list[0].AgentID)
}

func TestUpsertPresenceRejectsEmpty(t *testing.T) {
	s := openTest(t)
	assert.Err(t, s.UpsertPresence(PresenceRecord{AgentToken: "t", Name: "n", Status: "online"}))
	assert.Err(t, s.UpsertPresence(PresenceRecord{AgentID: "a", Name: "n", Status: "online"}))
}
