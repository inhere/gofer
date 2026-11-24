package inbox

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// KindSeatSchedule is the card kind of a seat schedule whose text had nobody to go to.
const KindSeatSchedule = today.KindSeatSchedule

// seatScheduleTextRunes bounds the excerpt of the schedule's text on the card.
const seatScheduleTextRunes = 80

// SeatScheduleSource is the slice of the job store the seat-schedule cards read.
type SeatScheduleSource interface {
	ScheduleSource
	GetSeat(id string) (jobstore.Seat, bool, error)
	GetAgentSession(id string) (jobstore.AgentSession, bool, error)
}

// SeatScheduleCards builds the cards of seat schedules whose last delivery found nobody to
// take the text. Everything is read from the store: the schedule's last result, and the
// seat's own session right now — so the card goes away once the session is online again, the
// next delivery lands, the schedule is disabled or the seat is archived, and nothing is held
// in memory. allow, when non-nil, is the caller's project scope.
func SeatScheduleCards(src SeatScheduleSource, lang i18n.Lang, allow func(string) bool) ([]today.Card, error) {
	list, err := src.ListSchedules("", true)
	if err != nil {
		return nil, err
	}
	cards := make([]today.Card, 0)
	for _, rec := range list {
		if len(cards) >= scheduleCardScan {
			break
		}
		seatID, ok := jobstore.SeatOfScheduleTarget(rec.Target)
		if !ok || !strings.HasPrefix(rec.LastResult, jobstore.ScheduleResultOffline) {
			continue
		}
		if allow != nil && !allow(rec.ProjectKey) {
			continue
		}
		x, found, err := src.GetSeat(seatID)
		if err != nil {
			return nil, err
		}
		if !found || x.ArchivedAt != 0 {
			continue
		}
		online, err := seatSessionOnline(src, x)
		if err != nil {
			return nil, err
		}
		if online {
			continue
		}
		cards = append(cards, today.Card{
			Key:          KindSeatSchedule + ":" + rec.ID,
			Kind:         KindSeatSchedule,
			Title:        i18n.T(lang, "httpapi.inbox_seat_schedule", "seat", x.Name, "name", rec.Name),
			Summary:      i18n.T(lang, "httpapi.inbox_seat_schedule_summary", "seat", x.Name, "text", scheduleTextExcerpt(rec.RequestJSON)),
			ProjectKey:   rec.ProjectKey,
			ActivityAt:   max(rec.LastRunAt, rec.UpdatedAt),
			WaitingSince: rec.LastRunAt,
			Refs:         today.Refs{ScheduleID: rec.ID, SeatID: x.ID},
			Actions: []today.Action{
				{ID: "open", Label: i18n.T(lang, "today.action.details"), Style: "link", Value: "/schedules"},
			},
		})
	}
	return cards, nil
}

// seatSessionOnline reports whether the seat has a session that can take a message now: a
// terminal session that is neither offline nor ended, or an ACP job that exists. Mirrors what
// seat.Service.DeliverToSeat treats as deliverable.
func seatSessionOnline(src SeatScheduleSource, x jobstore.Seat) (bool, error) {
	if x.CurrentRef == "" {
		return false, nil
	}
	if x.Kind == jobstore.SeatKindACP {
		return true, nil
	}
	a, ok, err := src.GetAgentSession(x.CurrentRef)
	if err != nil || !ok {
		return false, err
	}
	return a.State != jobstore.SessionOffline && a.State != jobstore.SessionEnded, nil
}

// scheduleTextExcerpt is the head of the text a schedule delivers (its stored request's prompt).
func scheduleTextExcerpt(requestJSON string) string {
	var req struct {
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal([]byte(requestJSON), &req)
	t := strings.Join(strings.Fields(req.Prompt), " ")
	if utf8.RuneCountInString(t) <= seatScheduleTextRunes {
		return t
	}
	return string([]rune(t)[:seatScheduleTextRunes]) + "…"
}
