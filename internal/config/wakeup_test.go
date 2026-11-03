package config

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestWakeupChildEventsConfigValidation: an unknown child-receipt event is refused at
// load time (arming a receipt for a typo would never fire), and validation does not
// normalize the list — ResolveChildEvents adds job.terminal back when it is used.
func TestWakeupChildEventsConfigValidation(t *testing.T) {
	ok := &Config{Wakeup: WakeupConfig{ChildEvents: []string{"job.needs_review", "job.terminal"}}}
	assert.NoErr(t, validate(ok))
	if len(ok.Wakeup.ChildEvents) != 2 {
		t.Fatalf("validate mutated child_events: %v", ok.Wakeup.ChildEvents)
	}

	bad := &Config{Wakeup: WakeupConfig{ChildEvents: []string{"job.terminal", "job.typo"}}}
	err := validate(bad)
	if err == nil || !strings.Contains(err.Error(), "wakeup.child_events[1]") || !strings.Contains(err.Error(), "job.typo") {
		t.Fatalf("validate err = %v, want wakeup.child_events[1] naming the unknown value", err)
	}
}

// TestResolveWakeupChildEvents: the three layers (per-submission override,
// wakeup.child_events, built-in default) resolve in that order, job.terminal is
// always present, duplicates collapse and an unknown value is refused.
func TestResolveWakeupChildEvents(t *testing.T) {
	def := (&Config{}).ResolveChildEvents
	got, err := def(nil)
	assert.NoErr(t, err)
	if strings.Join(got, ",") != "job.terminal,job.needs_review" {
		t.Fatalf("default = %v, want [job.terminal job.needs_review]", got)
	}

	// A configured set wins over the default, and job.terminal is put back even when
	// the operator left it out.
	cfg := &Config{Wakeup: WakeupConfig{ChildEvents: []string{"interaction.created"}}}
	got, err = cfg.ResolveChildEvents(nil)
	assert.NoErr(t, err)
	if strings.Join(got, ",") != "job.terminal,interaction.created" {
		t.Fatalf("configured = %v, want [job.terminal interaction.created]", got)
	}

	// A per-submission override wins over the configured set.
	got, err = cfg.ResolveChildEvents([]string{"job.awaiting_approval", "job.awaiting_approval"})
	assert.NoErr(t, err)
	if strings.Join(got, ",") != "job.terminal,job.awaiting_approval" {
		t.Fatalf("override = %v, want the override deduped under job.terminal", got)
	}

	// Unknown values are refused from either layer.
	if _, err := cfg.ResolveChildEvents([]string{"job.nope"}); err == nil {
		t.Fatal("an unknown override value was accepted")
	}
	bad := &Config{Wakeup: WakeupConfig{ChildEvents: []string{"job.nope"}}}
	if _, err := bad.ResolveChildEvents(nil); err == nil {
		t.Fatal("an unknown configured value was accepted at resolve time")
	}
}

// TestWakeupChildEventsClone: the list is deep-copied by Clone, so editing a clone's
// child events cannot change the live config the server reads.
func TestWakeupChildEventsClone(t *testing.T) {
	cfg := &Config{Wakeup: WakeupConfig{ChildEvents: []string{"job.terminal"}}}
	clone := cfg.Clone()
	clone.Wakeup.ChildEvents[0] = "interaction.created"
	if cfg.Wakeup.ChildEvents[0] != "job.terminal" {
		t.Fatalf("Clone shared the child_events slice: live = %v", cfg.Wakeup.ChildEvents)
	}
}
