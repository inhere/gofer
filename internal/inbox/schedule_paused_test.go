package inbox

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

type fakeScheduleSource struct{ list []jobstore.ScheduleRecord }

func (f fakeScheduleSource) ListSchedules(string, bool) ([]jobstore.ScheduleRecord, error) {
	return f.list, nil
}

// TestSchedulePausedCards: only a schedule paused by its failure streak AND still
// disabled becomes a card; hand-disabled, max_runs-paused and re-enabled schedules do
// not. The card is keyed per schedule, offers enable / open and files under 「失败」.
func TestSchedulePausedCards(t *testing.T) {
	src := fakeScheduleSource{list: []jobstore.ScheduleRecord{
		{ID: "paused", Name: "nightly", ProjectKey: "self", Enabled: 0, PausedReason: jobstore.SchedulePausedFailures, ConsecutiveFailures: 3, UpdatedAt: 500},
		{ID: "by-hand", Name: "x", ProjectKey: "self", Enabled: 0},
		{ID: "max-runs", Name: "y", ProjectKey: "self", Enabled: 0, PausedReason: jobstore.SchedulePausedMaxRuns},
		{ID: "re-enabled", Name: "z", ProjectKey: "self", Enabled: 1, PausedReason: jobstore.SchedulePausedFailures},
		{ID: "hidden", Name: "w", ProjectKey: "other", Enabled: 0, PausedReason: jobstore.SchedulePausedFailures},
	}}
	cards, err := SchedulePausedCards(src, i18n.ZhCN, func(p string) bool { return p == "self" })
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %+v, want exactly the paused one in scope", cards)
	}
	c := cards[0]
	if c.Key != "schedule.paused:paused" || c.Kind != today.KindSchedulePaused || c.Refs.ScheduleID != "paused" {
		t.Fatalf("card = %+v", c)
	}
	if len(c.Actions) != 2 || c.Actions[0].ID != "enable" || c.Actions[0].Value != "paused" || c.Actions[1].ID != "open" {
		t.Fatalf("actions = %+v, want enable + open", c.Actions)
	}
	if today.CanAdvise(c.Kind) {
		t.Fatal("advice must not be writable for a schedule.paused card")
	}

	waiting := Waiting(i18n.ZhCN, cards)
	if len(waiting) != 1 || waiting[0].Waiting != string(CatFailed) {
		t.Fatalf("waiting = %+v, want one entry in the failed class", waiting)
	}
	// Enabling the schedule (which clears the reason) removes the card.
	src.list[0].Enabled, src.list[0].PausedReason = 1, ""
	if cards, _ = SchedulePausedCards(src, i18n.ZhCN, nil); len(cards) != 1 || cards[0].Refs.ScheduleID != "hidden" {
		t.Fatalf("after enabling: %+v", cards)
	}
}
