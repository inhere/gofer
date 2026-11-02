package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestWorkbenchThreadPrefsRoundTripAndCallerIsolation(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertWorkbenchThreadPref(WorkbenchThreadPref{
		CallerID: "alice", ThreadID: "s:sess-1", Title: "renamed", SeenAt: 120, Pinned: true,
	}))
	assert.NoErr(t, s.UpsertWorkbenchThreadPref(WorkbenchThreadPref{
		CallerID: "bob", ThreadID: "s:sess-1", Title: "bob title",
	}))

	alice, err := s.ListWorkbenchThreadPrefs("alice")
	assert.NoErr(t, err)
	assert.Len(t, alice, 1)
	assert.Eq(t, "renamed", alice[0].Title)
	assert.Eq(t, int64(120), alice[0].SeenAt)
	assert.True(t, alice[0].Pinned)

	bob, err := s.ListWorkbenchThreadPrefs("bob")
	assert.NoErr(t, err)
	assert.Len(t, bob, 1)
	assert.Eq(t, "bob title", bob[0].Title)
	assert.Eq(t, int64(0), bob[0].SeenAt)
	assert.False(t, bob[0].Pinned)
}

func TestWorkbenchSnapshotIncludesWholeSessionAndBulkAttention(t *testing.T) {
	s := openTest(t)
	first := sampleJob("first", "alpha", 100)
	first.Status, first.SessionID, first.UpdatedAt = "done", "sess-1", 100
	first.EndedAt = 100
	latest := sampleJob("latest", "alpha", 1100)
	latest.Status, latest.SessionID, latest.ResumedFrom, latest.UpdatedAt = "running", "sess-1", "first", 1100
	activeOld := sampleJob("active-old", "alpha", 50)
	activeOld.Status, activeOld.UpdatedAt = "waiting_dir", 50
	doneOld := sampleJob("done-old", "alpha", 60)
	doneOld.Status, doneOld.EndedAt, doneOld.UpdatedAt = "done", 60, 60
	for _, rec := range []JobRecord{first, latest, activeOld, doneOld} {
		assert.NoErr(t, s.UpsertJob(rec))
	}
	assert.NoErr(t, s.UpsertInteraction(InteractionRecord{
		ID: "int-1", JobID: "latest", Type: "question", Prompt: "answer", Status: "pending", CreatedAt: 1050,
	}))
	_, err := s.InsertJobEvent(JobEvent{JobID: "latest", Type: "job.stalled", At: 1080})
	assert.NoErr(t, err)
	_, err = s.UpsertAgentSession(AgentSession{
		SessionID: "relay-1", Agent: "claude", ProjectKey: "alpha", Runner: "local", Cwd: ".",
		State: SessionWaitingReply, RelayMode: "on", StartedAt: 900, LastSeenAt: 1150,
	})
	assert.NoErr(t, err)
	relay := PlanDecision{
		ID: "relay-decision", Title: "relay", Question: "reply", State: DecisionOpen,
		SessionID: "relay-1", Kind: DecisionKindRelay, AskedAt: s.unixNow(), TimeoutSec: 3600,
	}
	assert.NoErr(t, s.InsertDecision(&relay))
	plan := Plan{
		PlanID: "plan-alpha-001", Title: "blocked", ProjectKey: "alpha", Status: PlanBlocked,
		CreatedAt: 1000, UpdatedAt: 1000,
	}
	assert.NoErr(t, s.InsertPlan(plan))
	decision := PlanDecision{
		ID: "plan-decision", PlanID: plan.PlanID, Title: "choose", Question: "which", State: DecisionOpen,
		AskedAt: s.unixNow(), TimeoutSec: 3600,
	}
	assert.NoErr(t, s.InsertDecision(&decision))
	assert.NoErr(t, s.UpsertWorkbenchThreadPref(WorkbenchThreadPref{
		CallerID: "alice", ThreadID: "s:sess-1", Pinned: true,
	}))

	snapshot, err := s.LoadWorkbenchSnapshot("alice", 1000, false)
	assert.NoErr(t, err)
	ids := make(map[string]bool, len(snapshot.Jobs))
	for _, rec := range snapshot.Jobs {
		ids[rec.ID] = true
	}
	assert.True(t, ids["first"])
	assert.True(t, ids["latest"])
	assert.True(t, ids["active-old"])
	assert.False(t, ids["done-old"])
	assert.Len(t, snapshot.PendingInteractions, 1)
	assert.True(t, snapshot.StalledJobIDs["latest"])
	assert.Len(t, snapshot.Sessions, 1)
	assert.Len(t, snapshot.RelayDecisions, 1)
	assert.Len(t, snapshot.BlockedPlans, 1)
	assert.Len(t, snapshot.OpenPlanDecisions, 1)
	assert.Len(t, snapshot.DecisionPlans, 1)
	assert.Len(t, snapshot.Prefs, 1)
}

func TestWorkbenchPrefsTableExistsOnOpen(t *testing.T) {
	s := openTest(t)
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='workbench_thread_prefs'`).Scan(&name)
	assert.NoErr(t, err)
	assert.Eq(t, "workbench_thread_prefs", name)
}

func TestWorkbenchSnapshotExcludesAckedRelayAttention(t *testing.T) {
	s := openTest(t)
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "relay-acked", Agent: "claude", State: SessionWaitingReply, RelayMode: RelayModeOn, StartedAt: 10, LastSeenAt: 20})
	assert.NoErr(t, err)
	d := PlanDecision{ID: "relay-acked-decision", Title: "relay", Question: "reply", State: DecisionOpen, SessionID: "relay-acked", Kind: DecisionKindRelay, AskedAt: 10, TimeoutSec: 3600}
	assert.NoErr(t, s.InsertDecision(&d))
	assert.NoErr(t, func() error { _, err := s.AckDecision(d.ID, "human"); return err }())
	snapshot, err := s.LoadWorkbenchSnapshot("alice", 0, false)
	assert.NoErr(t, err)
	assert.Len(t, snapshot.RelayDecisions, 0)
}

func TestWorkbenchLayoutStoreCompareAndSwapAndCallerIsolation(t *testing.T) {
	s := openTest(t)

	_, ok, err := s.GetWorkbenchLayout("alice")
	assert.NoErr(t, err)
	assert.False(t, ok)

	saved, updated, err := s.PutWorkbenchLayout("alice", 0, `{"future":{"kept":true}}`, 100)
	assert.NoErr(t, err)
	assert.True(t, updated)
	assert.Eq(t, int64(1), saved.Version)
	assert.Eq(t, `{"future":{"kept":true}}`, saved.BodyJSON)

	current, updated, err := s.PutWorkbenchLayout("alice", 0, `{"stale":true}`, 101)
	assert.NoErr(t, err)
	assert.False(t, updated)
	assert.Eq(t, int64(1), current.Version)
	assert.Eq(t, `{"future":{"kept":true}}`, current.BodyJSON)

	alice, ok, err := s.GetWorkbenchLayout("alice")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, saved, alice)
	_, ok, err = s.GetWorkbenchLayout("bob")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

func TestWorkbenchLayoutsTableExistsOnOpen(t *testing.T) {
	s := openTest(t)
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='workbench_layouts'`).Scan(&name)
	assert.NoErr(t, err)
	assert.Eq(t, "workbench_layouts", name)
}

func TestWorkbenchSnapshotInternalMessengerJobsOptIn(t *testing.T) {
	s := openTest(t)
	normal := sampleJob("normal", "alpha", 100)
	normal.Status, normal.UpdatedAt = "running", 100
	msgr := sampleJob("msgr", "alpha", 110)
	msgr.Status, msgr.UpdatedAt, msgr.TagsJSON = "running", 110, `["session-messenger"]`
	for _, rec := range []JobRecord{normal, msgr} {
		assert.NoErr(t, s.UpsertJob(rec))
	}
	ids := func(include bool) map[string]bool {
		snap, err := s.LoadWorkbenchSnapshot("alice", 0, include)
		assert.NoErr(t, err)
		out := map[string]bool{}
		for _, rec := range snap.Jobs {
			out[rec.ID] = true
		}
		return out
	}
	hidden, shown := ids(false), ids(true)
	assert.True(t, hidden["normal"])
	assert.False(t, hidden["msgr"])
	assert.True(t, shown["normal"])
	assert.True(t, shown["msgr"])
}

func TestLoadWorkbenchPageKeysWindowCursorAndExceptions(t *testing.T) {
	s := openTest(t)
	sess := func(id string, updated int64, state string) {
		rec := sampleJob("job-"+id, "alpha", updated-1)
		rec.Status, rec.SessionID, rec.UpdatedAt, rec.Interactive = state, id, updated, true
		assert.NoErr(t, s.UpsertJob(rec))
	}
	sess("new-a", 5000, "done")
	sess("new-b", 4000, "done")
	sess("old-a", 100, "done")
	sess("old-live", 90, "running")
	sess("old-legacy", 80, "done")
	// A batch job (neither interactive nor a resident session) is never a thread key.
	batch := sampleJob("batch", "alpha", 4500)
	batch.Status, batch.UpdatedAt = "done", 4500
	assert.NoErr(t, s.UpsertJob(batch))
	// An ended relay session outside the window; a live one inside it.
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "relay-old", Agent: "claude", State: SessionEnded, RelayMode: RelayModeOn, StartedAt: 10, LastSeenAt: 50})
	assert.NoErr(t, err)
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "relay-new", Agent: "claude", State: SessionIdle, RelayMode: RelayModeOn, StartedAt: 10, LastSeenAt: 4500})
	assert.NoErr(t, err)
	// UpsertAgentSession stamps last_seen_at with the clock; pin the fixtures' activity.
	for sid, seen := range map[string]int64{"relay-old": 50, "relay-new": 4500} {
		_, err := s.db.Exec(`UPDATE agent_sessions SET last_seen_at=? WHERE session_id=?`, seen, sid)
		assert.NoErr(t, err)
	}
	// The pin of a session thread may still be recorded on its first job ("j:<id>").
	assert.NoErr(t, s.UpsertWorkbenchThreadPref(WorkbenchThreadPref{CallerID: "alice", ThreadID: "j:job-old-legacy", Pinned: true}))

	ids := func(p WorkbenchPage) []string {
		var out []string
		for _, k := range p.Keys {
			out = append(out, k.ID)
		}
		return out
	}
	q := WorkbenchPageQuery{Since: 1000, Limit: 2, PinnedIDs: []string{"j:job-old-legacy"}}
	first, err := s.LoadWorkbenchPage("alice", q)
	assert.NoErr(t, err)
	// Exceptions first (a relay session that is not ended, a running job, the legacy-pinned
	// chain), newest first; then the ordinary threads inside the window.
	assert.Eq(t, []string{"r:relay-new", "s:old-live", "s:old-legacy", "s:new-a", "s:new-b"}, ids(first))
	assert.False(t, first.HasMore)
	assert.True(t, first.BelowFloor) // old-a and the ended relay session sit below the floor
	assert.Len(t, first.Snapshot.Jobs, 4)
	assert.Len(t, first.Snapshot.Sessions, 1)

	// A one-thread page reports more, and the cursor continues strictly after it.
	q.Limit = 1
	p1, err := s.LoadWorkbenchPage("alice", q)
	assert.NoErr(t, err)
	assert.True(t, p1.HasMore)
	assert.Eq(t, "s:new-a", p1.Keys[len(p1.Keys)-1].ID)
	// With a cursor and no floor: older ordinary threads only, no exceptions again.
	p2, err := s.LoadWorkbenchPage("alice", WorkbenchPageQuery{CursorAt: 4000, CursorID: "s:new-b", Limit: 10, PinnedIDs: q.PinnedIDs})
	assert.NoErr(t, err)
	assert.Eq(t, []string{"s:old-a", "r:relay-old"}, ids(p2))
	assert.False(t, p2.HasMore)
	// Project narrowing happens in the query.
	none, err := s.LoadWorkbenchPage("alice", WorkbenchPageQuery{Limit: 10, Project: "nope"})
	assert.NoErr(t, err)
	assert.Len(t, none.Keys, 0)
}

// TestLoadWorkbenchPageOfflineSessionStaysInItsWindow: an offline relay session stopped
// heart-beating and was never ended; it is not live, so outside the window it waits for
// "load more" instead of riding the first page (a deployment listed sessions offline for
// up to 30 days by default).
func TestLoadWorkbenchPageOfflineSessionStaysInItsWindow(t *testing.T) {
	s := openTest(t)
	for sid, st := range map[string]string{"relay-offline": SessionOffline, "relay-idle": SessionIdle} {
		_, err := s.UpsertAgentSession(AgentSession{SessionID: sid, Agent: "claude", State: st, RelayMode: RelayModeOn, StartedAt: 10, LastSeenAt: 50})
		assert.NoErr(t, err)
		_, err = s.db.Exec(`UPDATE agent_sessions SET last_seen_at=50, state=? WHERE session_id=?`, st, sid)
		assert.NoErr(t, err)
	}
	first, err := s.LoadWorkbenchPage("alice", WorkbenchPageQuery{Since: 1000, Limit: 10})
	assert.NoErr(t, err)
	var got []string
	for _, k := range first.Keys {
		got = append(got, k.ID)
	}
	assert.Eq(t, []string{"r:relay-idle"}, got)
	assert.True(t, first.BelowFloor)
}
