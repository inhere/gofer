package notify

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestDefaultTriggerEventsIncludePlanBlocked (PLAN-03, design §六 decision 1): a chain
// job failing parks the plan and nobody else will move it, so plan.blocked joins the
// default trigger set — while the other plan events stay opt-in — and its IM line names
// the item, the job and the reason the human has to act on.
func TestDefaultTriggerEventsIncludePlanBlocked(t *testing.T) {
	cfg := &config.NotificationConfig{
		Webhooks: []config.WebhookConfig{{URL: "https://a"}},
	}
	if got := urls(MatchWebhooks(cfg, "plan.blocked", "p")); len(got) != 1 {
		t.Errorf("plan.blocked => %v, want the default set to admit it", got)
	}
	for _, optIn := range []string{"plan.completed", "plan.todo_advanced", "plan.todo_unassigned", "plan.advance_paused"} {
		if got := urls(MatchWebhooks(cfg, optIn, "p")); len(got) != 0 {
			t.Errorf("%s should not match the default set, got %v", optIn, got)
		}
	}
	// The pre-PLAN-03 defaults still hold.
	for _, keep := range []string{"job.terminal", "interaction.created", "job.needs_review"} {
		if got := urls(MatchWebhooks(cfg, keep, "p")); len(got) != 1 {
			t.Errorf("%s => %v, want it still in the default set", keep, got)
		}
	}

	msg, ok := PlanMessage("plan.blocked", `{"todo_id":"todo-b","job":"job-1","reason":"exit status 1"}`, 7)
	if !ok {
		t.Fatal("PlanMessage must handle plan.blocked")
	}
	if msg.Title != "plan blocked" || msg.EventType != "plan.blocked" || msg.At != 7 {
		t.Fatalf("message = %+v", msg)
	}
	for _, want := range []string{"todo todo-b", "job job-1", "exit status 1"} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("message text %q missing %q", msg.Text, want)
		}
	}
	// A non-plan event is refused, so the caller falls back to the job shape.
	if _, ok := PlanMessage("job.terminal", `{}`, 1); ok {
		t.Fatal("PlanMessage must refuse a non-plan event")
	}
	// A malformed detail still renders a message naming the type, never an error.
	if _, ok := PlanMessage("plan.blocked", "{not json", 1); !ok {
		t.Fatal("a malformed detail must still render")
	}
}
