package inbox

import (
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

type fakeSeatSource struct {
	fakeScheduleSource
	seats    map[string]jobstore.Seat
	sessions map[string]jobstore.AgentSession
}

func (f fakeSeatSource) GetSeat(id string) (jobstore.Seat, bool, error) {
	x, ok := f.seats[id]
	return x, ok, nil
}

func (f fakeSeatSource) GetAgentSession(id string) (jobstore.AgentSession, bool, error) {
	a, ok := f.sessions[id]
	return a, ok, nil
}

func offlineSchedule(id, seatID string) jobstore.ScheduleRecord {
	return jobstore.ScheduleRecord{
		ID: id, Name: "weekly", ProjectKey: "self", Enabled: 1, Target: jobstore.ScheduleTargetSeat(seatID),
		LastResult: jobstore.ScheduleResultOffline, LastRunAt: 100, RequestJSON: `{"prompt":"send the weekly report"}`,
	}
}

// A seat schedule whose text had nobody to go to is a card of the attention class, derived from
// the stored facts: it goes away when the session is online again, when the next delivery lands,
// when the schedule is disabled and when the seat is archived.
func TestSeatScheduleCardLifecycle(t *testing.T) {
	src := fakeSeatSource{
		fakeScheduleSource: fakeScheduleSource{list: []jobstore.ScheduleRecord{offlineSchedule("sch-1", "seat-1")}},
		seats:              map[string]jobstore.Seat{"seat-1": {ID: "seat-1", Name: "desk", Kind: jobstore.SeatKindTerminal, CurrentRef: "s1"}},
		sessions:           map[string]jobstore.AgentSession{"s1": {SessionID: "s1", State: jobstore.SessionOffline}},
	}
	cards, err := SeatScheduleCards(src, i18n.ZhCN, nil)
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards = %+v, err %v", cards, err)
	}
	c := cards[0]
	if c.Key != "seat_schedule:sch-1" || c.Kind != today.KindSeatSchedule || c.Refs.SeatID != "seat-1" || c.Refs.ScheduleID != "sch-1" {
		t.Fatalf("card = %+v", c)
	}
	if today.CanAdvise(c.Kind) {
		t.Fatal("advice must not be writable for a seat_schedule card")
	}
	if w := Waiting(i18n.ZhCN, cards); len(w) != 1 || w[0].Waiting != string(CatAttention) {
		t.Fatalf("waiting = %+v, want one attention entry", w)
	}

	// Session comes back online: the card is gone.
	src.sessions["s1"] = jobstore.AgentSession{SessionID: "s1", State: jobstore.SessionWaitingReply}
	if cards, _ = SeatScheduleCards(src, i18n.ZhCN, nil); len(cards) != 0 {
		t.Fatalf("online session still shows a card: %+v", cards)
	}
	// Offline again, but the next delivery landed.
	src.sessions["s1"] = jobstore.AgentSession{SessionID: "s1", State: jobstore.SessionOffline}
	src.list[0].LastResult = jobstore.ScheduleResultDelivered
	if cards, _ = SeatScheduleCards(src, i18n.ZhCN, nil); len(cards) != 0 {
		t.Fatalf("delivered result still shows a card: %+v", cards)
	}
	// Archived seat.
	src.list[0].LastResult = jobstore.ScheduleResultOffline
	x := src.seats["seat-1"]
	x.ArchivedAt = 5
	src.seats["seat-1"] = x
	if cards, _ = SeatScheduleCards(src, i18n.ZhCN, nil); len(cards) != 0 {
		t.Fatalf("archived seat still shows a card: %+v", cards)
	}
}

func TestSeatScheduleCardVacantSeatAndScope(t *testing.T) {
	src := fakeSeatSource{
		fakeScheduleSource: fakeScheduleSource{list: []jobstore.ScheduleRecord{offlineSchedule("sch-1", "seat-1")}},
		seats:              map[string]jobstore.Seat{"seat-1": {ID: "seat-1", Name: "desk", Kind: jobstore.SeatKindTerminal}},
	}
	if cards, _ := SeatScheduleCards(src, i18n.ZhCN, nil); len(cards) != 1 {
		t.Fatalf("a vacant seat gets the card: %+v", cards)
	}
	if cards, _ := SeatScheduleCards(src, i18n.ZhCN, func(string) bool { return false }); len(cards) != 0 {
		t.Fatalf("out of scope: %+v", cards)
	}
}

// The kind is an inbox action kind and its notify event is registered, so it reaches /v1/inbox.
func TestSeatScheduleKindIsInboxAction(t *testing.T) {
	if got := config.InboxDefaultColumn(today.KindSeatSchedule); got != "action" {
		t.Fatalf("seat_schedule column = %q, want action", got)
	}
	if got := config.InboxKindEvents(today.KindSeatSchedule); !slices.Equal(got, []string{"schedule.seat_pending"}) {
		t.Fatalf("events = %v", got)
	}
}
