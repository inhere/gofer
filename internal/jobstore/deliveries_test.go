package jobstore

import (
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestEventDeliveriesTableExists proves Open creates the event_deliveries table
// (E14): PRAGMA table_info returns rows only for an existing table.
func TestEventDeliveriesTableExists(t *testing.T) {
	s := openTest(t)
	cols, err := s.tableColumns("event_deliveries")
	assert.NoErr(t, err)
	for _, c := range []string{
		"id", "event_seq", "job_id", "target", "status",
		"attempts", "next_retry_at", "last_error", "created_at", "updated_at",
	} {
		if _, ok := cols[c]; !ok {
			t.Fatalf("event_deliveries missing column %q (cols=%v)", c, cols)
		}
	}
}

func TestInsertDeliveryValidation(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertDelivery(Delivery{Target: "https://x", Status: DeliveryPending}); err == nil {
		t.Fatalf("expected error on empty job id")
	}
	if _, err := s.InsertDelivery(Delivery{JobID: "j1", Status: DeliveryPending}); err == nil {
		t.Fatalf("expected error on empty target")
	}
}

// TestClaimDueDeliveriesTakesDueNotFuture proves the sweeper claim only takes
// rows whose next_retry_at <= now and leaves future rows untouched. It also
// proves a claimed row is leased out of the due window (a second claim at the
// same now does NOT re-take it).
func TestClaimDueDeliveriesTakesDueNotFuture(t *testing.T) {
	s := openTest(t)
	dueID, err := s.InsertDelivery(Delivery{
		JobID: "j1", Target: "https://hooks/a", Status: DeliveryPending, NextRetryAt: 100, CreatedAt: 100,
	})
	assert.NoErr(t, err)
	futureID, err := s.InsertDelivery(Delivery{
		JobID: "j1", Target: "https://hooks/b", Status: DeliveryPending, NextRetryAt: 5000, CreatedAt: 100,
	})
	assert.NoErr(t, err)

	// now=200, lease=60: only the due row (next_retry_at=100) is claimed; it is
	// leased to 260 so an immediate re-claim at the same now skips it.
	claimed, err := s.ClaimDueDeliveries(200, 10, 60)
	assert.NoErr(t, err)
	assert.Len(t, claimed, 1)
	assert.Eq(t, dueID, claimed[0].ID)
	assert.Eq(t, int64(260), claimed[0].NextRetryAt)

	again, err := s.ClaimDueDeliveries(200, 10, 60)
	assert.NoErr(t, err)
	assert.Len(t, again, 0) // still leased, future row not yet due

	// After the lease lapses (and the future row's time arrives) both are due.
	later, err := s.ClaimDueDeliveries(6000, 10, 60)
	assert.NoErr(t, err)
	assert.Len(t, later, 2)
	_ = futureID
}

// TestClaimDueDeliveriesLimit caps the batch size.
func TestClaimDueDeliveriesLimit(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 5; i++ {
		_, err := s.InsertDelivery(Delivery{
			JobID: "j1", Target: "https://hooks/x", Status: DeliveryPending, NextRetryAt: 1, CreatedAt: 1,
		})
		assert.NoErr(t, err)
	}
	claimed, err := s.ClaimDueDeliveries(10, 2, 60)
	assert.NoErr(t, err)
	assert.Len(t, claimed, 2)

	none, err := s.ClaimDueDeliveries(10, 0, 60)
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
}

// TestDeliveryStateFlow exercises the three Mark* transitions.
func TestDeliveryStateFlow(t *testing.T) {
	s := openTest(t)
	id1, err := s.InsertDelivery(Delivery{JobID: "j1", Target: "https://a", Status: DeliveryPending, NextRetryAt: 1, CreatedAt: 1})
	assert.NoErr(t, err)
	id2, err := s.InsertDelivery(Delivery{JobID: "j1", Target: "https://b", Status: DeliveryPending, NextRetryAt: 1, CreatedAt: 1})
	assert.NoErr(t, err)
	id3, err := s.InsertDelivery(Delivery{JobID: "j1", Target: "https://c", Status: DeliveryPending, NextRetryAt: 1, CreatedAt: 1})
	assert.NoErr(t, err)

	assert.NoErr(t, s.MarkDelivered(id1, 50))
	assert.NoErr(t, s.MarkRetry(id2, 1, 80, "boom 500", 50))
	assert.NoErr(t, s.MarkFailed(id3, 6, "gave up", 50))

	all, err := s.ListDeliveriesByJob("j1")
	assert.NoErr(t, err)
	assert.Len(t, all, 3)

	byID := map[int64]Delivery{}
	for _, d := range all {
		byID[d.ID] = d
	}
	assert.Eq(t, DeliveryDelivered, byID[id1].Status)
	assert.Eq(t, "", byID[id1].LastError)

	assert.Eq(t, DeliveryPending, byID[id2].Status)
	assert.Eq(t, 1, byID[id2].Attempts)
	assert.Eq(t, int64(80), byID[id2].NextRetryAt)
	assert.Eq(t, "boom 500", byID[id2].LastError)

	assert.Eq(t, DeliveryFailed, byID[id3].Status)
	assert.Eq(t, 6, byID[id3].Attempts)
	assert.Eq(t, "gave up", byID[id3].LastError)

	// A delivered/failed row is no longer due even at a far-future now.
	due, err := s.ClaimDueDeliveries(1_000_000, 10, 60)
	assert.NoErr(t, err)
	// id2 stayed pending with next_retry_at=80, so it IS due far in the future;
	// id1 (delivered) and id3 (failed) are not.
	assert.Len(t, due, 1)
	assert.Eq(t, id2, due[0].ID)
}

// TestClaimDueDeliveriesNoDoubleClaim proves the SR303 single-claim guarantee:
// many concurrent ClaimDueDeliveries calls over the same due rows never hand the
// same delivery out twice (the sum of claimed ids equals the row set, with no
// duplicate).
func TestClaimDueDeliveriesNoDoubleClaim(t *testing.T) {
	s := openTest(t)
	const total = 40
	for i := 0; i < total; i++ {
		_, err := s.InsertDelivery(Delivery{
			JobID: "j1", Target: "https://hooks/x", Status: DeliveryPending, NextRetryAt: 1, CreatedAt: 1,
		})
		assert.NoErr(t, err)
	}

	var (
		mu   sync.Mutex
		seen = map[int64]int{}
		wg   sync.WaitGroup
	)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				claimed, err := s.ClaimDueDeliveries(10, 3, 60)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if len(claimed) == 0 {
					return
				}
				// Immediately mark delivered so a claimed row leaves the pending pool,
				// mirroring how the real sweep retires each claimed delivery.
				mu.Lock()
				for _, d := range claimed {
					seen[d.ID]++
				}
				mu.Unlock()
				for _, d := range claimed {
					_ = s.MarkDelivered(d.ID, 10)
				}
			}
		}()
	}
	wg.Wait()

	if len(seen) != total {
		t.Fatalf("expected %d distinct deliveries claimed, got %d", total, len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("delivery %d claimed %d times (want exactly 1)", id, n)
		}
	}
}
