package jobstore

import (
	"fmt"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// sampleInteraction builds a pending InteractionRecord for tests.
func sampleInteraction(id, jobID string, createdAt int64) InteractionRecord {
	return InteractionRecord{
		ID:          id,
		JobID:       jobID,
		Type:        "question",
		Prompt:      "continue?",
		OptionsJSON: `[{"value":"a","label":"A"}]`,
		Status:      "pending",
		CreatedAt:   createdAt,
	}
}

func TestUpsertInteractionRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleInteraction("i1", "job-1", 1000)
	assert.NoErr(t, s.UpsertInteraction(in))

	list, err := s.ListInteractions("job-1")
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	got := list[0]
	assert.Eq(t, "i1", got.ID)
	assert.Eq(t, "job-1", got.JobID)
	assert.Eq(t, "question", got.Type)
	assert.Eq(t, "continue?", got.Prompt)
	assert.Eq(t, `[{"value":"a","label":"A"}]`, got.OptionsJSON)
	assert.Eq(t, "pending", got.Status)
	assert.Eq(t, "", got.Answer)
	assert.Eq(t, int64(1000), got.CreatedAt)
	assert.Eq(t, int64(0), got.AnsweredAt)
	// 监督分层升级路由（supervisor-routing P1.1）：未 escalate 的 interaction escalated_at=0。
	assert.Eq(t, int64(0), got.EscalatedAt)
}

// TestUpsertInteractionEscalatedAtRoundTrip proves the escalated_at column
// (supervisor-routing P1.1) round-trips through upsert+read and the cross-job
// pending listing. P1.1 only adds the column + plumbing; the actual write happens
// in P1.2, but a record carrying a value must persist and read back identical.
func TestUpsertInteractionEscalatedAtRoundTrip(t *testing.T) {
	s := openTest(t)

	in := sampleInteraction("i-esc", "job-esc", 1000)
	in.EscalatedAt = 1700001234
	assert.NoErr(t, s.UpsertInteraction(in))

	list, err := s.ListInteractions("job-esc")
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, int64(1700001234), list[0].EscalatedAt)

	// Also visible via the cross-job pending listing (needs an active job).
	job := sampleJob("job-esc", "p", 900)
	job.Status = "running"
	assert.NoErr(t, s.UpsertJob(job))
	pending, err := s.ListPendingInteractions()
	assert.NoErr(t, err)
	assert.Len(t, pending, 1)
	assert.Eq(t, int64(1700001234), pending[0].EscalatedAt)
}

// TestUpsertInteractionIsCreateThenUpdate proves the pending and answered writes
// for one interaction are two upserts on ONE row (same job_id,id): the answered
// snapshot overwrites the pending one, keeping a single latest row.
func TestUpsertInteractionIsCreateThenUpdate(t *testing.T) {
	s := openTest(t)

	pending := sampleInteraction("i1", "job-1", 500)
	assert.NoErr(t, s.UpsertInteraction(pending))

	answered := pending
	answered.Status = "answered"
	answered.Answer = "yes"
	answered.AnsweredAt = 800
	assert.NoErr(t, s.UpsertInteraction(answered))

	list, err := s.ListInteractions("job-1")
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, "answered", list[0].Status)
	assert.Eq(t, "yes", list[0].Answer)
	assert.Eq(t, int64(800), list[0].AnsweredAt)
}

func TestListInteractionsOrderedByCreation(t *testing.T) {
	s := openTest(t)

	// Insert out of creation order; ListInteractions must return created_at asc.
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("c", "job-1", 300)))
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("a", "job-1", 100)))
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("b", "job-1", 200)))
	// A different job's interaction must not leak into the listing.
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("x", "job-2", 50)))

	list, err := s.ListInteractions("job-1")
	assert.NoErr(t, err)
	assert.Len(t, list, 3)
	assert.Eq(t, "a", list[0].ID) // created_at 100
	assert.Eq(t, "b", list[1].ID) // 200
	assert.Eq(t, "c", list[2].ID) // 300
}

func TestListInteractionsEmptyJob(t *testing.T) {
	s := openTest(t)
	list, err := s.ListInteractions("no-such-job")
	assert.NoErr(t, err)
	assert.Len(t, list, 0)
}

func TestUpsertInteractionRejectsEmptyIDs(t *testing.T) {
	s := openTest(t)
	assert.Err(t, s.UpsertInteraction(InteractionRecord{JobID: "j", Type: "question", Prompt: "p", Status: "pending"}))
	assert.Err(t, s.UpsertInteraction(InteractionRecord{ID: "i", Type: "question", Prompt: "p", Status: "pending"}))
}

// TestListPendingInteractionsActiveOnly proves the JOIN+filter (复审 #4) returns
// only pending interactions of NON-terminal jobs (a 僵尸 pending on a done job is
// excluded).
func TestListPendingInteractionsActiveOnly(t *testing.T) {
	s := openTest(t)

	active := sampleJob("job-active", "p", 100)
	active.Status = "running"
	assert.NoErr(t, s.UpsertJob(active))
	done := sampleJob("job-done", "p", 100)
	done.Status = "done"
	assert.NoErr(t, s.UpsertJob(done))

	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("i-live", "job-active", 200)))
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("i-zombie", "job-done", 200)))

	list, err := s.ListPendingInteractions()
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, "i-live", list[0].ID)
}

// TestCountSupPendingDemand proves the event-driven sup demand signal (y5wt): counts
// pending interactions of ACTIVE non-supervisor jobs that genuinely need a sup (L2) — and
// excludes terminal-job zombies, needs_human rows, supervisor-role jobs (套娃), AND
// interactions still legitimately with their OWNER within the owner-answer window. 0 demand
// ⇒ idle ⇒ no sup dispatched.
func TestCountSupPendingDemand(t *testing.T) {
	s := openTest(t)
	const ownerTimeout, now = int64(300), int64(10000)

	// active non-sup job, NO owner, fresh pending → sup demand (escalates straight to L2).
	active := sampleJob("job-active", "p", 100)
	active.Status = "running"
	assert.NoErr(t, s.UpsertJob(active))
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("i-live", "job-active", 200)))

	// terminal job's zombie pending → excluded (mirrors ListPendingInteractions).
	done := sampleJob("job-done", "p", 100)
	done.Status = "done"
	assert.NoErr(t, s.UpsertJob(done))
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("i-zombie", "job-done", 200)))

	// supervisor-role job's own pending interaction → excluded (套娃防护).
	supJob := sampleJob("job-sup", "p", 100)
	supJob.Status, supJob.Role = "running", "supervisor"
	assert.NoErr(t, s.UpsertJob(supJob))
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("i-sup", "job-sup", 200)))

	// owner-pending WITHIN window → excluded (the owner should answer, not a sup).
	owned := sampleJob("job-owned", "p", 100)
	owned.Status, owned.OriginAgent = "running", "agt_owner"
	assert.NoErr(t, s.UpsertJob(owned))
	itOwned := sampleInteraction("i-owned", "job-owned", 200)
	itOwned.EscalatedAt = now - 100 // escalated to owner 100s ago, window is 300s → still owner's
	assert.NoErr(t, s.UpsertInteraction(itOwned))

	n, err := s.CountSupPendingDemand(ownerTimeout, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, n) // only i-live (no-owner); owner-pending i-owned excluded within window

	// Owner answer window elapses → i-owned now becomes sup demand (owner-timeout fallback).
	itOwned.EscalatedAt = now - 400 // 400s ago > 300s window
	assert.NoErr(t, s.UpsertInteraction(itOwned))
	n, err = s.CountSupPendingDemand(ownerTimeout, now)
	assert.NoErr(t, err)
	assert.Eq(t, 2, n) // i-live + i-owned (owner timed out)

	// Once a sup punts a demand interaction to a human, it drops out (no re-wake loop).
	assert.NoErr(t, s.MarkInteractionNeedsHuman("job-active", "i-live"))
	assert.NoErr(t, s.MarkInteractionNeedsHuman("job-owned", "i-owned"))
	n, err = s.CountSupPendingDemand(ownerTimeout, now)
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
}

// TestMarkInteractionNeedsHuman proves the needs_human flag round-trips (targeted
// update, then read-back via ListInteractions) and is preserved across a later upsert
// (excluded.needs_human carries it), and that an unknown id is a silent no-op.
func TestMarkInteractionNeedsHuman(t *testing.T) {
	s := openTest(t)

	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("i1", "job-1", 1000)))
	assert.NoErr(t, s.MarkInteractionNeedsHuman("job-1", "i1"))

	list, err := s.ListInteractions("job-1")
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, int64(1), list[0].NeedsHuman)

	// A later full upsert carrying the flag must preserve it (not reset to 0).
	rec := list[0]
	rec.Status = "answered"
	assert.NoErr(t, s.UpsertInteraction(rec))
	list, err = s.ListInteractions("job-1")
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), list[0].NeedsHuman)

	// Unknown id is a silent no-op (0 rows, no error).
	assert.NoErr(t, s.MarkInteractionNeedsHuman("job-1", "nope"))
	assert.Err(t, s.MarkInteractionNeedsHuman("", "i1"))
}

// TestReconcileOrphanInteractions proves the startup backstop flips pending rows
// of terminal jobs to cancelled while leaving active-job pending rows untouched.
func TestReconcileOrphanInteractions(t *testing.T) {
	s := openTest(t)

	active := sampleJob("job-active", "p", 100)
	active.Status = "running"
	assert.NoErr(t, s.UpsertJob(active))
	done := sampleJob("job-done", "p", 100)
	done.Status = "done"
	assert.NoErr(t, s.UpsertJob(done))

	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("i-live", "job-active", 200)))
	assert.NoErr(t, s.UpsertInteraction(sampleInteraction("i-zombie", "job-done", 200)))

	n, err := s.ReconcileOrphanInteractions(9999)
	assert.NoErr(t, err)
	assert.Eq(t, 1, n) // only the zombie

	// The zombie is now cancelled with answered_at stamped.
	doneList, err := s.ListInteractions("job-done")
	assert.NoErr(t, err)
	assert.Len(t, doneList, 1)
	assert.Eq(t, "cancelled", doneList[0].Status)
	assert.Eq(t, int64(9999), doneList[0].AnsweredAt)

	// The active job's pending is untouched and still listed.
	pending, err := s.ListPendingInteractions()
	assert.NoErr(t, err)
	assert.Len(t, pending, 1)
	assert.Eq(t, "i-live", pending[0].ID)
}

// TestConcurrentInteractionUpserts exercises the writeMu concurrency contract for
// interactions: many goroutines upsert distinct rows while several hammer a single
// hot interaction (pending->answered churn). None must error, and the final row
// count per job must equal the distinct interaction count.
func TestConcurrentInteractionUpserts(t *testing.T) {
	s := openTest(t)

	const (
		writers    = 12
		perWriter  = 30
		hotUpdates = 60
	)

	errCh := make(chan error, writers*perWriter+hotUpdates)
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("w%02d-%03d", w, i)
				if err := s.UpsertInteraction(sampleInteraction(id, "hotjob", int64(w*1000+i))); err != nil {
					errCh <- err
				}
			}
		}(w)
	}

	// Hot interaction: all goroutines upsert the SAME (job_id,id) concurrently.
	for u := 0; u < hotUpdates; u++ {
		wg.Add(1)
		go func(u int) {
			defer wg.Done()
			rec := sampleInteraction("hot", "hotjob", 1)
			rec.Status = fmt.Sprintf("s%d", u)
			rec.Answer = fmt.Sprintf("ans%d", u)
			rec.AnsweredAt = int64(u)
			if err := s.UpsertInteraction(rec); err != nil {
				errCh <- err
			}
		}(u)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		assert.NoErr(t, err)
	}

	// distinct rows for hotjob = writers*perWriter + 1 hot row.
	list, err := s.ListInteractions("hotjob")
	assert.NoErr(t, err)
	assert.Eq(t, writers*perWriter+1, len(list))
}
