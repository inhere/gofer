package notify

import (
	"encoding/json"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func urls(ws []config.WebhookConfig) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.URL
	}
	return out
}

func TestMatchWebhooksDefaultTriggerSet(t *testing.T) {
	cfg := &config.NotificationConfig{
		Webhooks: []config.WebhookConfig{
			{URL: "https://a"}, // no events => default set (job.terminal + interaction.created)
		},
	}
	// job.terminal and interaction.created match the default set; job.running does not.
	if got := urls(MatchWebhooks(cfg, "job.terminal", "p")); len(got) != 1 {
		t.Errorf("job.terminal => %v", got)
	}
	if got := urls(MatchWebhooks(cfg, "interaction.created", "p")); len(got) != 1 {
		t.Errorf("interaction.created => %v", got)
	}
	if got := urls(MatchWebhooks(cfg, "job.running", "p")); len(got) != 0 {
		t.Errorf("job.running should not match default set, got %v", got)
	}
}

func TestMatchWebhooksExplicitEvents(t *testing.T) {
	cfg := &config.NotificationConfig{
		Webhooks: []config.WebhookConfig{
			{URL: "https://a", Events: []string{"job.running"}},
		},
	}
	if got := urls(MatchWebhooks(cfg, "job.running", "p")); len(got) != 1 {
		t.Errorf("explicit job.running => %v", got)
	}
	if got := urls(MatchWebhooks(cfg, "job.terminal", "p")); len(got) != 0 {
		t.Errorf("job.terminal not subscribed => %v", got)
	}
}

func TestMatchWebhooksProjectFilter(t *testing.T) {
	cfg := &config.NotificationConfig{
		Webhooks: []config.WebhookConfig{
			{URL: "https://a", Projects: []string{"proj-a"}},
			{URL: "https://b"}, // all projects
		},
	}
	got := urls(MatchWebhooks(cfg, "job.terminal", "proj-a"))
	if len(got) != 2 {
		t.Errorf("proj-a => %v (want both)", got)
	}
	got = urls(MatchWebhooks(cfg, "job.terminal", "proj-b"))
	if len(got) != 1 || got[0] != "https://b" {
		t.Errorf("proj-b => %v (want only the all-projects webhook)", got)
	}
}

func TestMatchWebhooksNilOrEmpty(t *testing.T) {
	if got := MatchWebhooks(nil, "job.terminal", "p"); got != nil {
		t.Errorf("nil cfg => %v", got)
	}
	if got := MatchWebhooks(&config.NotificationConfig{}, "job.terminal", "p"); got != nil {
		t.Errorf("no webhooks => %v", got)
	}
	// A webhook with an empty URL is skipped (defensive).
	cfg := &config.NotificationConfig{Webhooks: []config.WebhookConfig{{URL: ""}}}
	if got := MatchWebhooks(cfg, "job.terminal", "p"); len(got) != 0 {
		t.Errorf("empty url webhook should be skipped, got %v", got)
	}
}

func TestBuildBody(t *testing.T) {
	job := JobSummary{ID: "j1", Status: "failed", Project: "p", Agent: "claude", Runner: "local", ExitCode: 1}
	b, err := BuildBody(7, "j1", "job.terminal", `{"status":"failed","exit_code":1}`, 1234, job)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Event.Seq != 7 || p.Event.Type != "job.terminal" || p.Event.At != 1234 {
		t.Errorf("event = %+v", p.Event)
	}
	if p.Job.ID != "j1" || p.Job.Status != "failed" || p.Job.ExitCode != 1 {
		t.Errorf("job = %+v", p.Job)
	}
	// detail round-trips as embedded JSON.
	var d map[string]any
	if err := json.Unmarshal(p.Event.Detail, &d); err != nil {
		t.Fatalf("detail not valid json: %v", err)
	}
	if d["status"] != "failed" {
		t.Errorf("detail.status = %v", d["status"])
	}
}

// TestBuildBodyInvalidDetailDropped proves a non-JSON detail is dropped (the body
// still serialises with seq/type/at).
func TestBuildBodyInvalidDetailDropped(t *testing.T) {
	b, err := BuildBody(1, "j1", "job.running", "not-json", 1, JobSummary{ID: "j1"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.Event.Detail) != 0 {
		t.Errorf("invalid detail should be dropped, got %s", p.Event.Detail)
	}
}
