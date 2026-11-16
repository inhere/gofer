package inbox

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/today"
)

// TestHealthCardsOnePerFailingItem: one card per failing self-check item, keyed by the item
// name, titled with the item's human name and the streak, carrying the details action, filed
// under the attention class, and gone as soon as the item's run succeeds (no card is built
// for it any more).
func TestHealthCardsOnePerFailingItem(t *testing.T) {
	items := []HealthItem{
		{Name: "disk", Failures: 3, LastError: "low disk space: 200MiB free", At: 1_800_000_000},
		{Name: "notification", Failures: 4, LastError: "delivery failing: webhook (4 in a row)", At: 1_800_000_060},
	}
	cards := HealthCards(i18n.EN, items)
	if len(cards) != 2 {
		t.Fatalf("cards = %+v, want one per failing item", cards)
	}
	disk := cards[0]
	if disk.Key != "server.health:disk" || disk.Kind != today.KindServerHealth {
		t.Fatalf("card = %+v, want key server.health:disk of kind %q", disk, today.KindServerHealth)
	}
	if !strings.Contains(disk.Title, "Free space of the data directory") || !strings.Contains(disk.Title, "3 times in a row") {
		t.Errorf("title = %q, want the item's name and the streak", disk.Title)
	}
	if disk.Summary != "low disk space: 200MiB free" || disk.ActivityAt != 1_800_000_000 {
		t.Errorf("summary / activity = %q / %d", disk.Summary, disk.ActivityAt)
	}
	if len(disk.Actions) != 1 || disk.Actions[0].ID != "open" || disk.Actions[0].Value != HealthLink {
		t.Errorf("actions = %+v, want the details link to %s", disk.Actions, HealthLink)
	}
	if today.CanAdvise(disk.Kind) {
		t.Error("advice must not be writable for a server health card")
	}

	waiting := Waiting(i18n.EN, cards)
	if len(waiting) != 2 || waiting[0].Waiting != string(CatAttention) || waiting[0].WaitingLabel == "" {
		t.Fatalf("waiting = %+v, want both items in the attention class", waiting)
	}

	// One success drops the item from the failing list: its card disappears with it.
	if left := HealthCards(i18n.EN, []HealthItem{{Name: "notification", Failures: 4}}); len(left) != 1 || left[0].Key != "server.health:notification" {
		t.Fatalf("after recovery = %+v, want only the notification card", left)
	}
	if none := HealthCards(i18n.EN, nil); none == nil || len(none) != 0 {
		t.Fatalf("no failing items = %#v, want an empty list", none)
	}
}

// TestHealthCardNameFallsBackToIdentifier: the check names are wire identifiers, so a check
// added later without a translation still gets a readable card instead of a i18n key.
func TestHealthCardNameFallsBackToIdentifier(t *testing.T) {
	cards := HealthCards(i18n.ZhCN, []HealthItem{{Name: "brand_new_check", Failures: 2}})
	if len(cards) != 1 {
		t.Fatalf("cards = %+v", cards)
	}
	if title := cards[0].Title; !strings.Contains(title, "brand_new_check") || strings.Contains(title, "httpapi.") {
		t.Fatalf("title = %q, want the unknown check named as is", title)
	}
	// A known item is translated in both languages.
	if zh := HealthCards(i18n.ZhCN, []HealthItem{{Name: "storage", Failures: 3}}); !strings.Contains(zh[0].Title, "存储可写") {
		t.Fatalf("zh title = %q, want the Chinese name", zh[0].Title)
	}
}
