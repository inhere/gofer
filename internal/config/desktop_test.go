package config

import (
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// TestDesktopNotifyConfigDecodes pins the server.notification.desktop block by its yaml
// names (gofer-3wce): a drifted tag would silently disable the channel.
func TestDesktopNotifyConfigDecodes(t *testing.T) {
	const src = `
server:
  notification:
    desktop:
      enabled: true
      events: [job.awaiting_approval, session.awaiting_reply]
      projects: [proj-a]
      merge_sec: 30
projects:
  proj-a:
    host_path: /x
`
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(src), cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ApplyDefaults(cfg)
	if err := validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	d := cfg.Server.Notification.Desktop
	if d == nil {
		t.Fatal("server.notification.desktop is nil")
	}
	if !d.IsEnabled() {
		t.Error("desktop.enabled = true but IsEnabled() = false")
	}
	if len(d.Events) != 2 || d.Events[1] != "session.awaiting_reply" {
		t.Errorf("desktop.events = %v", d.Events)
	}
	if len(d.Projects) != 1 || d.Projects[0] != "proj-a" {
		t.Errorf("desktop.projects = %v", d.Projects)
	}
	if got := d.EffectiveMergeSec(); got != 30 {
		t.Errorf("EffectiveMergeSec = %d, want 30", got)
	}
}

// An absent block is off; a present block with no fields is on with the default
// window; merge_sec: 0 disables merging.
func TestDesktopNotifyConfigDefaults(t *testing.T) {
	if (&NotificationConfig{}).Desktop.IsEnabled() {
		t.Error("desktop channel with no block = enabled, want off")
	}
	if got := (&NotificationConfig{}).Desktop.EffectiveMergeSec(); got != DefaultDesktopMergeSec {
		t.Errorf("EffectiveMergeSec with no block = %d, want %d", got, DefaultDesktopMergeSec)
	}
	zero := 0
	d := &DesktopNotifyConfig{MergeSec: &zero}
	if !d.IsEnabled() {
		t.Error("desktop block with no enabled switch = off, want on")
	}
	if got := d.EffectiveMergeSec(); got != 0 {
		t.Errorf("EffectiveMergeSec with merge_sec 0 = %d, want 0", got)
	}
	off := false
	if (&DesktopNotifyConfig{Enabled: &off}).IsEnabled() {
		t.Error("desktop.enabled false = on, want off")
	}
}

// A negative merge_sec fails validation: the coalescing window is a duration, and 0 is
// the documented way to say "no merging".
func TestDesktopNotifyConfigRejectsNegativeMergeSec(t *testing.T) {
	neg := -1
	cfg := &Config{Server: ServerConfig{Notification: &NotificationConfig{
		Desktop: &DesktopNotifyConfig{MergeSec: &neg},
	}}}
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "desktop.merge_sec") {
		t.Fatalf("validate = %v, want a merge_sec error", err)
	}
}

// Clone must deep-copy the desktop block: a reload writes through one generation while
// the other still serves the previous one.
func TestDesktopNotifyConfigCloned(t *testing.T) {
	sec := 12
	cfg := &Config{Server: ServerConfig{Notification: &NotificationConfig{
		Desktop: &DesktopNotifyConfig{Events: []string{"job.needs_review"}, Projects: []string{"p"}, MergeSec: &sec},
	}}}
	clone := cfg.Clone()
	clone.Server.Notification.Desktop.Events[0] = "job.terminal"
	clone.Server.Notification.Desktop.Projects[0] = "other"
	*clone.Server.Notification.Desktop.MergeSec = 99

	orig := cfg.Server.Notification.Desktop
	if orig.Events[0] != "job.needs_review" || orig.Projects[0] != "p" || *orig.MergeSec != 12 {
		t.Errorf("original desktop block was mutated through the clone: %+v", orig)
	}
}
