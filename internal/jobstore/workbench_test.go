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

	snapshot, err := s.LoadWorkbenchSnapshot("alice", 1000)
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
	assert.Len(t, snapshot.Prefs, 1)
}

func TestWorkbenchPrefsTableExistsOnOpen(t *testing.T) {
	s := openTest(t)
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='workbench_thread_prefs'`).Scan(&name)
	assert.NoErr(t, err)
	assert.Eq(t, "workbench_thread_prefs", name)
}
