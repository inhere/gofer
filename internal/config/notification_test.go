package config

import (
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// TestNotificationConfigDecodes verifies the E14 server.notification block and
// the project-level notify_enabled flag map onto the structs by their yaml names.
// A drifted tag here would silently drop the webhook config, so this guards it.
func TestNotificationConfigDecodes(t *testing.T) {
	const src = `
server:
  notification:
    max_text_runes: 1234
    webhooks:
      - url: https://hooks.example.com/gofer
        max_text_runes: 77
        events: [job.terminal, interaction.created]
        secret_env: GOFER_WEBHOOK_SECRET
        projects: [proj-a]
      - url: https://other.example.com/hook
    allow_hosts: [hooks.example.com, other.example.com]
    allow_http: true
    max_attempts: 4
projects:
  proj-a:
    host_path: /x
    notify_enabled: false
  proj-b:
    host_path: /y
`
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(src), cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ApplyDefaults(cfg)
	if err := validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	n := cfg.Server.Notification
	if n == nil {
		t.Fatal("server.notification is nil")
	}
	if len(n.Webhooks) != 2 {
		t.Fatalf("webhooks len = %d", len(n.Webhooks))
	}
	w0 := n.Webhooks[0]
	if w0.URL != "https://hooks.example.com/gofer" {
		t.Errorf("webhook[0].url = %q", w0.URL)
	}
	if len(w0.Events) != 2 || w0.Events[0] != "job.terminal" || w0.Events[1] != "interaction.created" {
		t.Errorf("webhook[0].events = %v", w0.Events)
	}
	if w0.SecretEnv != "GOFER_WEBHOOK_SECRET" {
		t.Errorf("webhook[0].secret_env = %q", w0.SecretEnv)
	}
	if len(w0.Projects) != 1 || w0.Projects[0] != "proj-a" {
		t.Errorf("webhook[0].projects = %v", w0.Projects)
	}
	if len(n.AllowHosts) != 2 {
		t.Errorf("allow_hosts = %v", n.AllowHosts)
	}
	if !n.AllowHTTP {
		t.Error("allow_http should decode true")
	}
	if n.MaxAttempts != 4 || n.EffectiveMaxAttempts() != 4 {
		t.Errorf("max_attempts = %d (effective %d)", n.MaxAttempts, n.EffectiveMaxAttempts())
	}
	if n.MaxTextRunes != 1234 || n.EffectiveMaxTextRunes() != 1234 {
		t.Errorf("max_text_runes = %d (effective %d)", n.MaxTextRunes, n.EffectiveMaxTextRunes())
	}
	if got := w0.EffectiveMaxTextRunes(n.EffectiveMaxTextRunes()); got != 77 {
		t.Errorf("webhook max_text_runes = %d, want 77", got)
	}

	pa := cfg.Projects["proj-a"]
	if pa.NotifyEnabled == nil || *pa.NotifyEnabled {
		t.Errorf("proj-a notify_enabled should decode false, got %v", pa.NotifyEnabled)
	}
	if pa.IsNotifyEnabled() {
		t.Error("proj-a IsNotifyEnabled should be false")
	}
	pb := cfg.Projects["proj-b"]
	if pb.NotifyEnabled != nil {
		t.Errorf("proj-b notify_enabled should stay nil, got %v", pb.NotifyEnabled)
	}
	if !pb.IsNotifyEnabled() {
		t.Error("proj-b IsNotifyEnabled should default true")
	}
}

// TestEffectiveMaxAttemptsDefault checks the unset/nil paths default to 6.
func TestEffectiveMaxAttemptsDefault(t *testing.T) {
	var n *NotificationConfig
	if got := n.EffectiveMaxAttempts(); got != DefaultMaxAttempts {
		t.Errorf("nil EffectiveMaxAttempts = %d, want %d", got, DefaultMaxAttempts)
	}
	n2 := &NotificationConfig{}
	if got := n2.EffectiveMaxAttempts(); got != DefaultMaxAttempts {
		t.Errorf("zero EffectiveMaxAttempts = %d, want %d", got, DefaultMaxAttempts)
	}
}

func TestEffectiveMaxTextRunesDefault(t *testing.T) {
	var n *NotificationConfig
	if got := n.EffectiveMaxTextRunes(); got != DefaultMaxTextRunes {
		t.Errorf("nil EffectiveMaxTextRunes = %d, want %d", got, DefaultMaxTextRunes)
	}
	n2 := &NotificationConfig{}
	if got := n2.EffectiveMaxTextRunes(); got != DefaultMaxTextRunes {
		t.Errorf("zero EffectiveMaxTextRunes = %d, want %d", got, DefaultMaxTextRunes)
	}
	if got := (WebhookConfig{}).EffectiveMaxTextRunes(123); got != 123 {
		t.Errorf("inherited webhook max_text_runes = %d, want 123", got)
	}
}
