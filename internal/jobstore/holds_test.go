package jobstore

import (
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// heldJob builds an awaiting_approval row expiring at expiresAt.
func heldJob(id string, expiresAt int64) JobRecord {
	rec := sampleJob(id, "proj", 100)
	rec.Status = statusAwaitingApproval
	rec.HoldExpiresAt = expiresAt
	rec.HoldJSON = `{"reason":"push"}`
	return rec
}

// TestHoldColumnsRoundTrip: the hold pair survives an upsert/read and a later upsert
// (the approved run's own persists) keeps whatever the writer carries.
func TestHoldColumnsRoundTrip(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertJob(heldJob("h1", 500)))
	got, ok, err := s.GetJob("h1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(500), got.HoldExpiresAt)
	assert.Eq(t, `{"reason":"push"}`, got.HoldJSON)
	assert.Eq(t, statusAwaitingApproval, got.Status)
}

// TestDecideHoldIsCompareAndSet: only the first decision on an awaiting row wins; a
// terminal decision stamps ended_at + error, an approval (-> queued) does not.
func TestDecideHoldIsCompareAndSet(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertJob(heldJob("h1", 500)))
	assert.NoErr(t, s.UpsertJob(heldJob("h2", 500)))

	won, err := s.DecideHold(HoldDecision{ID: "h1", To: "queued", HoldJSON: `{"decision":"approved"}`, At: 300})
	assert.NoErr(t, err)
	assert.True(t, won)
	again, err := s.DecideHold(HoldDecision{ID: "h1", To: "cancelled", Error: "hold expired", At: 301})
	assert.NoErr(t, err)
	assert.False(t, again, "a decided hold must not be decided twice")
	got, _, _ := s.GetJob("h1")
	assert.Eq(t, "queued", got.Status)
	assert.Eq(t, int64(0), got.EndedAt)
	assert.Eq(t, "", got.Error)
	assert.Eq(t, `{"decision":"approved"}`, got.HoldJSON)

	won, err = s.DecideHold(HoldDecision{ID: "h2", To: "cancelled", HoldJSON: `{"decision":"rejected"}`, Error: "hold rejected by alice", At: 302})
	assert.NoErr(t, err)
	assert.True(t, won)
	got, _, _ = s.GetJob("h2")
	assert.Eq(t, "cancelled", got.Status)
	assert.Eq(t, int64(302), got.EndedAt)
	assert.Eq(t, "hold rejected by alice", got.Error)

	// An explicit From lets the approve path fail its own queued row.
	won, err = s.DecideHold(HoldDecision{ID: "h1", From: "queued", To: "failed", Error: "approved but could not start", At: 303})
	assert.NoErr(t, err)
	assert.True(t, won)

	missing, err := s.DecideHold(HoldDecision{ID: "nope", To: "queued", At: 1})
	assert.NoErr(t, err)
	assert.False(t, missing)
}

// TestDecideHoldConcurrentSingleWinner: N racing decisions on one row, one winner.
func TestDecideHoldConcurrentSingleWinner(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertJob(heldJob("h1", 500)))
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			to := "queued"
			if i%2 == 1 {
				to = "cancelled"
			}
			ok, err := s.DecideHold(HoldDecision{ID: "h1", To: to, At: int64(300 + i)})
			if err != nil {
				t.Errorf("DecideHold: %v", err)
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	assert.Eq(t, 1, wins)
}

// TestListDueHolds: only awaiting rows whose deadline passed, oldest first, bounded.
func TestListDueHolds(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertJob(heldJob("late", 200)))
	assert.NoErr(t, s.UpsertJob(heldJob("early", 100)))
	assert.NoErr(t, s.UpsertJob(heldJob("future", 900)))
	decided := heldJob("decided", 50)
	decided.Status = "cancelled"
	assert.NoErr(t, s.UpsertJob(decided))
	plain := sampleJob("plain", "proj", 100) // never held: hold_expires_at 0
	assert.NoErr(t, s.UpsertJob(plain))

	due, err := s.ListDueHolds(200, 10)
	assert.NoErr(t, err)
	ids := make([]string, 0, len(due))
	for _, r := range due {
		ids = append(ids, r.ID)
	}
	assert.Eq(t, []string{"early", "late"}, ids)

	one, err := s.ListDueHolds(1000, 1)
	assert.NoErr(t, err)
	assert.Len(t, one, 1)
	assert.Eq(t, "early", one[0].ID)
}

// TestReconcileOrphanJobsLeavesHolds: a held job is pure database state, so a restart
// must not fail it (it is not in nonTerminalJobStatuses / orphanWorkerJobStatuses).
func TestReconcileOrphanJobsLeavesHolds(t *testing.T) {
	s := openTest(t)
	held := heldJob("h1", 500)
	held.WorkerID = "w1" // even a worker-routed hold is not "in flight" on a worker
	assert.NoErr(t, s.UpsertJob(held))
	assert.NoErr(t, s.UpsertJob(heldJob("h2", 500)))
	n, err := s.ReconcileOrphanJobs(1000, "restart", []string{"worker"})
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
	for _, id := range []string{"h1", "h2"} {
		got, _, _ := s.GetJob(id)
		assert.Eq(t, statusAwaitingApproval, got.Status)
	}
}
