package notify

import (
	"slices"
	"testing"
)

// Every event a channel receives by DEFAULT must be described in the catalog: adding a
// member to DefaultTriggerEvents / DefaultDesktopEvents without a description would leave
// the console picker unable to explain it.
func TestEventCatalogCoversDefaults(t *testing.T) {
	if got := len(EventCatalog()); got != len(catalogEvents) {
		t.Fatalf("EventCatalog() returned %d entries, want %d", got, len(catalogEvents))
	}
	for _, key := range append(slices.Clone(DefaultTriggerEvents), DefaultDesktopEvents...) {
		assertCatalogHas(t, key)
	}
}

// The default (desktop included) flags come from the matcher sets, not from a second list.
func TestEventCatalogDefaultFlags(t *testing.T) {
	for _, e := range EventCatalog() {
		if want := slices.Contains(DefaultTriggerEvents, e.Key); e.Default != want {
			t.Errorf("%s: Default = %v, want %v", e.Key, e.Default, want)
		}
		if want := slices.Contains(DefaultDesktopEvents, e.Key); e.DesktopDefault != want {
			t.Errorf("%s: DesktopDefault = %v, want %v", e.Key, e.DesktopDefault, want)
		}
	}
}

// Keys are unique, every entry has both descriptions, and its group is a known one — the
// picker renders groups from EventGroups and would silently drop an orphan entry.
func TestEventCatalogIsWellFormed(t *testing.T) {
	known := make(map[string]bool, len(EventGroups))
	for _, g := range EventGroups {
		if g.Key == "" || g.Zh == "" || g.En == "" {
			t.Errorf("event group %+v is missing a key or a description", g)
		}
		known[g.Key] = true
	}
	seen := make(map[string]bool, len(catalogEvents))
	for _, e := range catalogEvents {
		if e.Key == "" {
			t.Error("catalog entry with an empty key")
			continue
		}
		if seen[e.Key] {
			t.Errorf("catalog entry %q appears twice", e.Key)
		}
		seen[e.Key] = true
		if e.Zh == "" || e.En == "" {
			t.Errorf("%s: missing a zh or en description", e.Key)
		}
		if !known[e.Group] {
			t.Errorf("%s: unknown group %q", e.Key, e.Group)
		}
	}
}

// The catalog is a copy: mutating what a caller got back must not change the catalog.
func TestEventCatalogReturnsCopy(t *testing.T) {
	first := EventCatalog()
	first[0].Key = "mutated"
	if EventCatalog()[0].Key == "mutated" {
		t.Fatal("EventCatalog() handed out the package's own slice")
	}
}

func assertCatalogHas(t *testing.T, key string) {
	t.Helper()
	if !slices.ContainsFunc(EventCatalog(), func(e EventInfo) bool { return e.Key == key }) {
		t.Errorf("default event %q has no catalog entry", key)
	}
}

// The default subscriptions are a compatibility surface: a webhook or desktop channel with
// no `events` filter receives exactly these. The inbox rules (server.inbox.rules) must never
// move them, so they are pinned literally; changing a set is a deliberate act that edits
// this test and the CHANGELOG.
func TestDefaultEventSetsAreLocked(t *testing.T) {
	wantWebhook := []string{"job.terminal", "interaction.created", "job.needs_review", "job.awaiting_approval", "plan.blocked", "job.retry_exhausted", "job.budget_exceeded", "plan.leader_exhausted", "session.awaiting_reply", "work.remind", "work.digest", "work.needs_me", "job.delivery_failed", "update.available", "job.child_wake_failed"}
	wantDesktop := []string{"job.awaiting_approval", "interaction.created", "session.awaiting_reply", "job.needs_review", "job.child_wake_failed"}
	if !slices.Equal(DefaultTriggerEvents, wantWebhook) {
		t.Fatalf("DefaultTriggerEvents = %v, want %v", DefaultTriggerEvents, wantWebhook)
	}
	if !slices.Equal(DefaultDesktopEvents, wantDesktop) {
		t.Fatalf("DefaultDesktopEvents = %v, want %v", DefaultDesktopEvents, wantDesktop)
	}
	for _, e := range EventCatalog() {
		if e.Default != slices.Contains(wantWebhook, e.Key) || e.DesktopDefault != slices.Contains(wantDesktop, e.Key) {
			t.Errorf("catalog flags of %q drifted: default=%v desktop=%v", e.Key, e.Default, e.DesktopDefault)
		}
	}
}
