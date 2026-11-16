package inbox

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

type fakeCallers struct{ rows []jobstore.StoredCaller }

func (f fakeCallers) ListStoredCallers() ([]jobstore.StoredCaller, error) { return f.rows, nil }

// TestCallerTokenCardsWindowAndWording: a card appears 7 days before the expiry, the wording
// changes once the expiry has passed, callers that never expire / are disabled / are taken
// over never show, the soonest expiry is first, and the card files under the attention class
// with the rotate and details actions.
func TestCallerTokenCardsWindowAndWording(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	day := int64(24 * 3600)
	src := fakeCallers{rows: []jobstore.StoredCaller{
		{ID: "far", ExpiresAt: now.Unix() + 8*day},
		{ID: "never"},
		{ID: "off", ExpiresAt: now.Unix() + day, DisabledAt: 1},
		{ID: "taken", ExpiresAt: now.Unix() + day, SupersededAt: 1},
		{ID: "soon", Name: "CI", ExpiresAt: now.Unix() + 3*day - 60},
		{ID: "edge", ExpiresAt: now.Unix() + 7*day},
		{ID: "gone", ExpiresAt: now.Unix() - 3600},
	}}
	cards, err := CallerTokenCards(src, i18n.EN, now)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range cards {
		ids = append(ids, c.Refs.CallerID)
	}
	if strings.Join(ids, ",") != "gone,soon,edge" {
		t.Fatalf("cards = %v, want gone,soon,edge (soonest first; far / never / off / taken hidden)", ids)
	}
	gone, soon := cards[0], cards[1]
	if gone.Kind != today.KindCallerTokenExpiring || gone.Key != "caller.token_expiring:gone" {
		t.Errorf("card = %+v", gone)
	}
	if !strings.Contains(gone.Title, "has expired") || strings.Contains(soon.Title, "has expired") || !strings.Contains(soon.Title, "3 day") || !strings.Contains(soon.Title, "CI (soon)") {
		t.Errorf("wording: expired %q, expiring %q", gone.Title, soon.Title)
	}
	if len(soon.Actions) != 2 || soon.Actions[0].ID != "rotate" || soon.Actions[0].Value != "soon" || soon.Actions[1].ID != "open" {
		t.Errorf("actions = %+v, want rotate + details", soon.Actions)
	}
	if today.CanAdvise(soon.Kind) {
		t.Error("advice must not be writable for a token card")
	}
	waiting := Waiting(i18n.EN, cards)
	if len(waiting) != 3 || waiting[0].Waiting != string(CatAttention) || waiting[0].WaitingLabel == "" {
		t.Fatalf("waiting = %+v, want three entries in the attention class", waiting)
	}
	if Rank(CatAttention) != Rank(CatFailed)+1 || Rank(CatDelivery) != Rank(CatAttention)+1 {
		t.Errorf("attention must sit right after failed and before delivery: %v", Categories)
	}
}
