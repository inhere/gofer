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
