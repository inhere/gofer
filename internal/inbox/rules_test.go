package inbox

import (
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/notify"
	"github.com/inhere/gofer/internal/today"
)

// notInboxEvents lists the notify catalog events that deliberately never reach the inbox,
// each with the reason. Adding an event to the notify catalog fails
// TestNotifyCatalogEventsAreClassified until it is mapped in config.inboxKindEvents or
// listed here: that is the "catalog grew, who decides its column" guard.
var notInboxEvents = map[string]string{
	"job.reviewed":              "outcome of a review a person just made; nothing left to decide",
	"job.hold_approved":         "outcome of an approval; the held job moves on by itself",
	"job.hold_rejected":         "outcome of an approval; informational on the job page",
	"job.hold_expired":          "informational; the job card already carries the final state",
	"job.stalled":               "ops alert for a running job, surfaced by lanes, not an inbox item",
	"job.budget_exceeded":       "ops alert delivered by webhook / desktop; the failed job itself reaches the inbox as job.failed",
	"job.retry_scheduled":       "progress of an automatic retry",
	"job.retry_started":         "progress of an automatic retry",
	"job.retry_exhausted":       "ops alert; the failed job itself reaches the inbox as job.failed",
	"job.delivery_failed":       "delivery problem shown on the job page and by webhook",
	"job.delivered":             "informational delivery result",
	"job.verify_finished":       "verification result shown on the job page",
	"job.uncommitted":           "informational hint on the job page",
	"job.fell_back":             "the follow-up job carries the story; the failed job is classified job.terminal",
	"job.auto_resumed":          "the follow-up job carries the story; the failed job is classified job.terminal",
	"job.agent_substituted":     "informational",
	"job.wakeup_failed":         "informational",
	"job.ceiling_denied":        "informational audit event",
	"job.dir_wait_timeout":      "informational",
	"interaction.answered":      "outcome of an answer; nothing left to decide",
	"interaction.punted":        "outcome of an answer; the question is back in the queue as interaction",
	"session.waiting":           "informational wait notice; the reply request is session.awaiting_reply",
	"session.attention":         "ops alert delivered by webhook",
	"session.nudge_paused":      "informational",
	"plan.completed":            "informational outcome",
	"plan.todo_unassigned":      "plan page / webhook concern",
	"plan.leader_exhausted":     "ops alert delivered by webhook; the blocked plan reaches the inbox as plan_blocked",
	"plan.todo_dispatch_failed": "plan page / webhook concern",
	"plan.advance_paused":       "plan page / webhook concern",
	notify.EventUpdateAvailable: "system notice, not tied to a work item",
	"config.updated":            "system audit event",
}

// Every notify catalog event is either announced to the inbox (config.InboxEventKinds) or
// excluded with a reason, never both, and the whitelist holds no stale key.
func TestNotifyCatalogEventsAreClassified(t *testing.T) {
	catalog := map[string]bool{}
	for _, e := range notify.EventCatalog() {
		catalog[e.Key] = true
		kinds := config.InboxEventKinds(e.Key)
		reason, excluded := notInboxEvents[e.Key]
		switch {
		case len(kinds) == 0 && !excluded:
			t.Errorf("notify event %q reaches no inbox kind and is not in notInboxEvents: map it in config.inboxKindEvents or whitelist it with a reason", e.Key)
		case len(kinds) > 0 && excluded:
			t.Errorf("notify event %q is mapped to %v but also whitelisted (%s)", e.Key, kinds, reason)
		case excluded && reason == "":
			t.Errorf("notInboxEvents[%q] needs a reason", e.Key)
		}
	}
	for k := range notInboxEvents {
		if !catalog[k] {
			t.Errorf("notInboxEvents names %q, which is not in notify.EventCatalog", k)
		}
	}
}

// The reverse direction: every event the inbox claims must exist in the notify catalog, and
// every inbox kind announced by an event must be a real kind with a default column.
func TestInboxEventsExistInNotifyCatalog(t *testing.T) {
	catalog := map[string]bool{}
	for _, e := range notify.EventCatalog() {
		catalog[e.Key] = true
	}
	for _, ev := range config.InboxEvents() {
		if !catalog[ev] {
			t.Errorf("inbox event %q is not in notify.EventCatalog", ev)
		}
		for _, k := range config.InboxEventKinds(ev) {
			if config.InboxDefaultColumn(k) == "" {
				t.Errorf("event %q announces unknown kind %q", ev, k)
			}
		}
	}
	for _, k := range config.InboxKinds() {
		for _, ev := range config.InboxKindEvents(k) {
			if !catalog[ev] {
				t.Errorf("kind %q names event %q, which is not in notify.EventCatalog", k, ev)
			}
		}
	}
}

// Desktop is the narrow, interrupting channel: whatever it pops by default must be work
// that is in the action column by default. The webhook default may be wider (job.terminal,
// work.digest are fyi) and that is by design; the sets themselves are locked in
// notify.TestDefaultEventSetsAreLocked.
func TestDesktopDefaultEventsAreActionKinds(t *testing.T) {
	for _, ev := range notify.DefaultDesktopEvents {
		kinds := config.InboxEventKinds(ev)
		if len(kinds) == 0 {
			t.Errorf("desktop default event %q reaches no inbox kind", ev)
		}
		for _, k := range kinds {
			if col := config.InboxDefaultColumn(k); col != ColumnAction && k != "job.terminal" {
				t.Errorf("desktop default event %q -> kind %q is in column %q, want action", ev, k, col)
			}
		}
	}
}

// Webhook defaults that can land in fyi are the documented exception to "fyi is not pushed":
// pin them so a change in either direction is a conscious one.
func TestWebhookDefaultFYIEventsArePinned(t *testing.T) {
	var fyi []string
	for _, ev := range notify.DefaultTriggerEvents {
		kinds := config.InboxEventKinds(ev)
		if slices.ContainsFunc(kinds, func(k string) bool { return config.InboxDefaultColumn(k) == ColumnFYI }) {
			fyi = append(fyi, ev)
		}
	}
	slices.Sort(fyi)
	if want := []string{"job.terminal", "work.digest"}; !slices.Equal(fyi, want) {
		t.Fatalf("webhook default events that can be fyi = %v, want %v", fyi, want)
	}
}

func TestDirectoryDefaultAndConfigSource(t *testing.T) {
	d := Directory(nil)
	if !d.Enabled || d.Source != SourceDefault || d.Version == "" || len(d.Rules) != 2 {
		t.Fatalf("default directory = %+v", d)
	}
	kinds := map[string]KindInfo{}
	for _, k := range d.Kinds {
		kinds[k.Kind] = k
		if k.Source != SourceDefault || k.Column != k.DefaultColumn {
			t.Fatalf("default kind %+v", k)
		}
	}
	if len(kinds) != len(config.InboxKinds()) || kinds["review"].Column != ColumnAction || kinds["review"].Events[0] != "job.needs_review" {
		t.Fatalf("kinds = %+v", kinds)
	}
	events := map[string]EventInfo{}
	for _, e := range d.Events {
		events[e.Key] = e
	}
	if len(events) != len(notify.EventCatalog()) || !slices.Equal(events["job.needs_review"].Kinds, []string{"review"}) || events["job.reviewed"].Kinds == nil || len(events["job.reviewed"].Kinds) != 0 {
		t.Fatalf("events view wrong: %+v", events["job.reviewed"])
	}
	if !events["job.needs_review"].DesktopDefault || !events["job.terminal"].Default || events["job.terminal"].DesktopDefault {
		t.Fatal("channel default flags not passed through")
	}

	cfg := &config.InboxConfig{Rules: []config.InboxRule{{ID: "r-fyi", Column: ColumnFYI, Events: []string{"job.needs_review"}, Priority: 5}}}
	d2 := Directory(cfg)
	if d2.Source != SourceConfig || d2.Version == d.Version || len(d2.Rules) != 1 {
		t.Fatalf("config directory = %+v", d2)
	}
	for _, k := range d2.Kinds {
		switch k.Kind {
		case "review":
			if k.Column != ColumnFYI || k.DefaultColumn != ColumnAction || k.Source != SourceConfig || k.RuleID != "r-fyi" {
				t.Fatalf("review = %+v", k)
			}
		case "interaction":
			if k.Column != "" || k.Source != SourceConfig {
				t.Fatalf("unlisted kind = %+v", k)
			}
		}
	}
	off := false
	if Directory(&config.InboxConfig{Enabled: &off}).Enabled {
		t.Fatal("enabled=false not reflected")
	}
}

func TestColumnMatchesRuleEvents(t *testing.T) {
	cfg := &config.InboxConfig{Rules: []config.InboxRule{
		{ID: "fail-fyi", Column: ColumnFYI, Events: []string{"job.terminal"}, Priority: 10},
		{ID: "rest", Column: ColumnAction, Kinds: []string{"review"}, Priority: 5},
	}}
	if got := Column(todayCard("job.failed"), cfg); got != ColumnFYI {
		t.Fatalf("job.failed via event = %q", got)
	}
	if got := Column(todayCard("review"), cfg); got != ColumnAction {
		t.Fatalf("review = %q", got)
	}
}

func todayCard(kind string) today.Card { return today.Card{Kind: kind} }
