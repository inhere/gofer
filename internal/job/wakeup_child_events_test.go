package job

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestWakeupEventCatalogCoversChildEvents pins the two halves of the child-receipt
// event vocabulary together: internal/config repeats the literals because internal/job
// imports it (G022), and every type a config list accepts must be one the wakeup
// matcher can actually subscribe to (design 2026-11-03 回执事件).
func TestWakeupEventCatalogCoversChildEvents(t *testing.T) {
	pairs := map[string]string{
		config.WakeupChildEventTerminal:           EventJobTerminal,
		config.WakeupChildEventNeedsReview:        EventJobNeedsReview,
		config.WakeupChildEventAwaitingApproval:   EventJobAwaitingApproval,
		config.WakeupChildEventInteractionCreated: EventInteractionCreated,
	}
	for cfgName, jobName := range pairs {
		if cfgName != jobName {
			t.Fatalf("config child event %q != job event %q", cfgName, jobName)
		}
		if !wakeupEventCatalog[jobName] {
			t.Fatalf("job event %q is not in the wakeup event catalog", jobName)
		}
	}
	if got := config.WakeupChildEvents(); len(got) != len(pairs) {
		t.Fatalf("config.WakeupChildEvents() = %v, want the %d pinned types", got, len(pairs))
	}
	// The default set stays the two outcomes: the "needs a person" events cost a parent
	// round each, so they are opt-in.
	def := config.DefaultWakeupChildEvents
	if len(def) != 2 || !contains(def, EventJobTerminal) || !contains(def, EventJobNeedsReview) {
		t.Fatalf("default child events = %v, want [job.terminal job.needs_review]", def)
	}
}

// TestInjectInteractionRecordsCreatedOnce: a bridged (worker / peer) interaction must
// show up in the job's timeline like a local one — that event is what the notification
// channels and a supervising parent job listen on — and exactly ONCE, even when a
// restarted serve re-bridges the same id (design 2026-11-03 事件).
func TestInjectInteractionRecordsCreatedOnce(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it := Interaction{
		ID: "peer-int-created", JobID: jobID, Type: InteractionTypeQuestion,
		Prompt: "continue?", Status: InteractionPending, CreatedAt: s.nowFn().Unix(),
	}
	if err := s.injectInteraction(jobID, it); err != nil {
		t.Fatalf("injectInteraction: %v", err)
	}
	if got := countInteractionCreated(t, s, jobID); got != 1 {
		t.Fatalf("interaction.created rows = %d, want one for the bridged interaction", got)
	}

	// The same id again (the peer resends `open`): the in-memory guard stops it.
	if err := s.injectInteraction(jobID, it); err != nil {
		t.Fatalf("repeat injectInteraction: %v", err)
	}
	if got := countInteractionCreated(t, s, jobID); got != 1 {
		t.Fatalf("interaction.created rows = %d after a repeat inject, want one", got)
	}

	// A restarted serve has no in-memory record: the DB check must still keep it at one.
	entry := s.entry(jobID)
	entry.mu.Lock()
	entry.interactions = nil
	entry.mu.Unlock()
	if err := s.injectInteraction(jobID, it); err != nil {
		t.Fatalf("injectInteraction after a restart: %v", err)
	}
	if got := countInteractionCreated(t, s, jobID); got != 1 {
		t.Fatalf("interaction.created rows = %d after a re-bridge, want one", got)
	}

	// A DIFFERENT interaction on the same job is its own event.
	second := it
	second.ID = "peer-int-2"
	if err := s.injectInteraction(jobID, second); err != nil {
		t.Fatalf("injectInteraction(second): %v", err)
	}
	if got := countInteractionCreated(t, s, jobID); got != 2 {
		t.Fatalf("interaction.created rows = %d, want one per interaction", got)
	}
}

// countInteractionCreated counts the interaction.created events on a job.
func countInteractionCreated(t *testing.T, s *Service, jobID string) int {
	t.Helper()
	events, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == EventInteractionCreated {
			n++
		}
	}
	return n
}
