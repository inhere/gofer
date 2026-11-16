package inbox

import (
	"strconv"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/today"
)

// KindServerHealth is the card kind of a server self-check item that stayed broken past
// its consecutive-failure threshold (design 2026-11-14 §5).
const KindServerHealth = today.KindServerHealth

// HealthLink is the web target the card's 「看详情」 action opens: the settings page, where
// the self-check items and their threshold live.
const HealthLink = "/settings"

// HealthItem is one FAILING self-check item as the card needs it — the slice of
// healthcheck.Result a card reads, declared here so the inbox (business layer) does not
// import the self-check loop (orchestration layer, G022). The caller passes only the items
// whose ConsecutiveFailures reached the threshold.
type HealthItem struct {
	// Name is the stable check identifier ("storage", "disk", "sweep", "notification").
	Name string
	// Failures counts the consecutive failed runs.
	Failures int
	// LastError is the latest failure's message.
	LastError string
	// At is the unix second of the latest run.
	At int64
}

// HealthCards builds one card per failing self-check item. The card carries no project and
// no reference to a row: the item's own name is the whole subject, and the card is keyed by
// it (`server.health:<name>`), so it disappears by itself as soon as the item's next run
// succeeds — there is no row to clear and nothing to acknowledge.
func HealthCards(lang i18n.Lang, items []HealthItem) []today.Card {
	cards := make([]today.Card, 0, len(items))
	for _, it := range items {
		if it.Name == "" {
			continue
		}
		cards = append(cards, today.Card{
			Key:          KindServerHealth + ":" + it.Name,
			Kind:         KindServerHealth,
			Title:        i18n.T(lang, "httpapi.inbox_server_health", "name", healthLabel(lang, it.Name), "failures", strconv.Itoa(it.Failures)),
			Summary:      it.LastError,
			ActivityAt:   it.At,
			WaitingSince: it.At,
			Actions: []today.Action{
				{ID: "open", Label: i18n.T(lang, "today.action.details"), Style: "link", Value: HealthLink},
			},
		})
	}
	return cards
}

// healthLabel is the human name of a check item. The check names are stable identifiers on
// the wire, so a translation is looked up by key and an unknown name (a check added later)
// falls back to the identifier itself rather than to a missing key.
func healthLabel(lang i18n.Lang, name string) string {
	key := "httpapi.health_check." + name
	if i18n.HasKey(lang, key) || i18n.HasKey(i18n.EN, key) {
		return i18n.T(lang, key)
	}
	return name
}
