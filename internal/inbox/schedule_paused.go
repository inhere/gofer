package inbox

import (
	"fmt"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// KindSchedulePaused is the card kind of a schedule gofer paused after too many
// consecutive failed runs (design 2026-11-12 §1, plan R6).
const KindSchedulePaused = today.KindSchedulePaused

// scheduleCardScan bounds how many paused schedules one request turns into cards.
const scheduleCardScan = 100

// ScheduleSource is the slice of the job store the paused-schedule cards read.
type ScheduleSource interface {
	ListSchedules(projectFilter string, enabledOnly bool) ([]jobstore.ScheduleRecord, error)
}

// SchedulePausedCards builds the cards of schedules gofer stopped by itself because
// max_failures consecutive runs failed. The condition is read from the schedule row —
// paused_reason=failures AND still disabled — so enabling the schedule (which clears
// the reason) removes the card, and a schedule someone disabled by hand never shows.
// allow, when non-nil, is the caller's project scope. Newest first.
func SchedulePausedCards(src ScheduleSource, lang i18n.Lang, allow func(string) bool) ([]today.Card, error) {
	list, err := src.ListSchedules("", false)
	if err != nil {
		return nil, err
	}
	cards := make([]today.Card, 0)
	for _, rec := range list {
		if len(cards) >= scheduleCardScan {
			break
		}
		if rec.Enabled != 0 || rec.PausedReason != jobstore.SchedulePausedFailures {
			continue
		}
		if allow != nil && !allow(rec.ProjectKey) {
			continue
		}
		lastJob := "-"
		if r, ok, gerr := lastScheduleJob(src, rec.ID); gerr == nil && ok {
			lastJob = r
		}
		cards = append(cards, today.Card{
			Key:          KindSchedulePaused + ":" + rec.ID,
			Kind:         KindSchedulePaused,
			Title:        i18n.T(lang, "httpapi.inbox_schedule_paused", "name", rec.Name, "failures", fmt.Sprint(rec.ConsecutiveFailures)),
			Summary:      i18n.T(lang, "httpapi.inbox_schedule_paused_summary", "job", lastJob),
			ProjectKey:   rec.ProjectKey,
			ActivityAt:   rec.UpdatedAt,
			WaitingSince: rec.UpdatedAt,
			Refs:         today.Refs{ScheduleID: rec.ID},
			Actions: []today.Action{
				{ID: "enable", Label: i18n.T(lang, "today.action.enable_schedule"), Style: "primary", Value: rec.ID},
				{ID: "open", Label: i18n.T(lang, "today.action.details"), Style: "link", Value: "/schedules"},
			},
		})
	}
	return cards, nil
}

// lastScheduleJob names the schedule's most recent job when the source can tell.
func lastScheduleJob(src ScheduleSource, scheduleID string) (string, bool, error) {
	l, ok := src.(interface {
		LatestScheduleJob(string) (jobstore.JobRecord, bool, error)
	})
	if !ok {
		return "", false, nil
	}
	r, found, err := l.LatestScheduleJob(scheduleID)
	if err != nil || !found {
		return "", false, err
	}
	return r.ID, true, nil
}
