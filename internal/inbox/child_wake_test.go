package inbox

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// fakeChildWakeSource is the store slice the child-receipt alert cards read.
type fakeChildWakeSource struct {
	events []jobstore.JobEvent
	jobs   map[string]jobstore.JobRecord
	now    int64
}

func (f *fakeChildWakeSource) ListJobEventsByType(eventType string, sinceAt int64) ([]jobstore.JobEvent, error) {
	out := make([]jobstore.JobEvent, 0, len(f.events))
	for _, ev := range f.events {
		if ev.Type == eventType && ev.At >= sinceAt {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (f *fakeChildWakeSource) GetJob(id string) (jobstore.JobRecord, bool, error) {
	rec, ok := f.jobs[id]
	return rec, ok, nil
}

func (f *fakeChildWakeSource) UnixNow() int64 { return f.now }

// TestChildWakeCards: the alert cards are built from the job.child_wake_failed events,
// one per stranded receipt, pointing at the PARENT job, inside the same 72-hour window
// as the failed-job cards and filtered by the caller's project scope. They are action
// cards that the 「等我」 list shows and advice cannot be written for.
func TestChildWakeCards(t *testing.T) {
	const now = int64(1_900_000_000)
	src := &fakeChildWakeSource{
		now: now,
		jobs: map[string]jobstore.JobRecord{
			"parent-1": {ID: "parent-1", ProjectKey: "self"},
			"parent-2": {ID: "parent-2", ProjectKey: "other"},
		},
		events: []jobstore.JobEvent{
			{Seq: 3, JobID: "parent-1", Type: "job.child_wake_failed",
				Detail: `{"wakeup_id":"wk-1","child_job_id":"child-1","reason":"parent_ended"}`, At: now - 60},
			{Seq: 2, JobID: "parent-2", Type: "job.child_wake_failed",
				Detail: `{"wakeup_id":"wk-2","child_job_id":"child-2","reason":"expired"}`, At: now - 120},
			// Older than the window: history, not something to act on.
			{Seq: 1, JobID: "parent-1", Type: "job.child_wake_failed",
				Detail: `{"wakeup_id":"wk-3","child_job_id":"child-3","reason":"expired"}`, At: now - failedWindowSec - 10},
		},
	}

	cards, err := ChildWakeCards(src, i18n.ZhCN, nil)
	if err != nil {
		t.Fatalf("ChildWakeCards: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("cards = %+v, want one per alert inside the window", cards)
	}
	first := cards[0]
	if first.Kind != today.KindChildWakeFailed || first.Key != "job.child_wake:wk-1" || first.Refs.JobID != "parent-1" {
		t.Fatalf("card = %+v, want a child-wake card about the parent job", first)
	}
	if first.ProjectKey != "self" || first.ActivityAt != now-60 || first.WaitingSince != now-60 {
		t.Fatalf("card = %+v, want the parent's project and the alert time", first)
	}
	if len(first.Actions) != 1 || first.Actions[0].Value != "/jobs/parent-1" {
		t.Fatalf("actions = %+v, want the one link to the parent job", first.Actions)
	}
	if first.Summary == "" {
		t.Fatal("card has no reason summary")
	}
	if today.CanAdvise(first.Kind) {
		t.Fatal("advice can be written for a child-wake card")
	}

	// The 「等我」 list shows it, under the failed category, one entry per parent.
	waiting := Waiting(i18n.ZhCN, cards)
	if len(waiting) != 2 || waiting[0].Waiting == "" {
		t.Fatalf("waiting = %+v, want both cards with a category", waiting)
	}

	// The caller's project scope filters it.
	only := func(project string) bool { return project == "other" }
	cards, err = ChildWakeCards(src, i18n.ZhCN, only)
	if err != nil {
		t.Fatalf("ChildWakeCards(scoped): %v", err)
	}
	if len(cards) != 1 || cards[0].Refs.JobID != "parent-2" {
		t.Fatalf("scoped cards = %+v, want only the allowed project's alert", cards)
	}
}
