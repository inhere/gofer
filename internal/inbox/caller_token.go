package inbox

import (
	"fmt"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// KindCallerTokenExpiring is the card kind of a stored caller whose token expires soon or
// already has (design 2026-11-14 §4, plan R9).
const KindCallerTokenExpiring = today.KindCallerTokenExpiring

// CallerTokenWarnWindow is how long before the expiry the card appears.
const CallerTokenWarnWindow = 7 * 24 * time.Hour

// CallerSource is the slice of the job store the token-expiry cards read.
type CallerSource interface {
	ListStoredCallers() ([]jobstore.StoredCaller, error)
}

// CallerTokenCards builds the cards of stored callers whose token expires within
// CallerTokenWarnWindow of now, or is already past its expiry. A disabled or taken-over
// caller never shows (nothing is left to rotate for it), and a caller with no expiry
// neither. The wording changes once the expiry has passed: 「expires within N days」 before,
// 「has expired」 from the expiry on. Soonest expiry first. Rotating the token (which resets
// the expiry unless the operator keeps it) or disabling the caller removes the card.
func CallerTokenCards(src CallerSource, lang i18n.Lang, now time.Time) ([]today.Card, error) {
	rows, err := src.ListStoredCallers()
	if err != nil {
		return nil, err
	}
	var due []jobstore.StoredCaller
	for _, r := range rows {
		if r.ExpiresAt == 0 || r.DisabledAt != 0 || r.SupersededAt != 0 {
			continue
		}
		if time.Unix(r.ExpiresAt, 0).Sub(now) > CallerTokenWarnWindow {
			continue
		}
		due = append(due, r)
	}
	// Soonest first (stable by id for equal stamps).
	for i := 1; i < len(due); i++ {
		for j := i; j > 0 && callerDueBefore(due[j], due[j-1]); j-- {
			due[j], due[j-1] = due[j-1], due[j]
		}
	}
	cards := make([]today.Card, 0, len(due))
	for _, r := range due {
		name := r.ID
		if r.Name != "" {
			name = r.Name + " (" + r.ID + ")"
		}
		expiry := time.Unix(r.ExpiresAt, 0)
		when := expiry.Format("2006-01-02 15:04")
		title, summary := "", ""
		if !expiry.After(now) {
			title = i18n.T(lang, "httpapi.inbox_caller_token_expired", "name", name)
			summary = i18n.T(lang, "httpapi.inbox_caller_token_expired_summary", "when", when)
		} else {
			days := int((expiry.Sub(now) + 24*time.Hour - time.Second) / (24 * time.Hour))
			title = i18n.T(lang, "httpapi.inbox_caller_token_expiring", "name", name, "days", fmt.Sprint(days))
			summary = i18n.T(lang, "httpapi.inbox_caller_token_expiring_summary", "when", when)
		}
		since := expiry.Add(-CallerTokenWarnWindow).Unix()
		cards = append(cards, today.Card{
			Key:          KindCallerTokenExpiring + ":" + r.ID,
			Kind:         KindCallerTokenExpiring,
			Title:        title,
			Summary:      summary,
			ActivityAt:   since,
			WaitingSince: since,
			Refs:         today.Refs{CallerID: r.ID},
			Actions: []today.Action{
				{ID: "rotate", Label: i18n.T(lang, "today.action.rotate_token"), Style: "primary", Value: r.ID},
				{ID: "open", Label: i18n.T(lang, "today.action.details"), Style: "link", Value: "/settings/callers"},
			},
		})
	}
	return cards, nil
}

func callerDueBefore(a, b jobstore.StoredCaller) bool {
	if a.ExpiresAt != b.ExpiresAt {
		return a.ExpiresAt < b.ExpiresAt
	}
	return a.ID < b.ID
}
