package inbox

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/today"
)

func TestColumnDefaultsAndExplicitRules(t *testing.T) {
	for _, kind := range []string{"interaction", "review", "capture"} {
		if got := Column(today.Card{Kind: kind}, nil); got != ColumnAction {
			t.Fatalf("%s = %q", kind, got)
		}
	}
	if got := Column(today.Card{Kind: "job.terminal"}, nil); got != ColumnFYI {
		t.Fatalf("terminal = %q", got)
	}
	cfg := &config.InboxConfig{Rules: []config.InboxRule{{ID: "only-fyi", Column: ColumnFYI, Kinds: []string{"review"}, Priority: 10}}}
	if got := Column(today.Card{Kind: "review"}, cfg); got != ColumnFYI {
		t.Fatalf("override = %q", got)
	}
	if got := Column(today.Card{Kind: "interaction"}, cfg); got != "" {
		t.Fatalf("unlisted kind = %q", got)
	}
}

func TestNewItemRevisionAndState(t *testing.T) {
	card := today.Card{Key: "review:j1", Kind: "review", Title: "review", ActivityAt: 10}
	item, ok := NewItem(card, nil, State{})
	if !ok || item.State != "unread" || item.SourceRevision == "" {
		t.Fatalf("item = %+v ok=%v", item, ok)
	}
	seen, _ := NewItem(card, nil, State{Seen: true})
	if seen.State != "seen" {
		t.Fatalf("seen state = %q", seen.State)
	}
	changed, _ := NewItem(today.Card{Key: card.Key, Kind: card.Kind, Title: card.Title, ActivityAt: 11}, nil, State{})
	if changed.SourceRevision == item.SourceRevision {
		t.Fatal("activity change did not change revision")
	}
}
