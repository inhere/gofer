package notify

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func desktopCfg(events, projects []string) *config.NotificationConfig {
	return &config.NotificationConfig{Desktop: &config.DesktopNotifyConfig{Events: events, Projects: projects}}
}

// An empty events filter uses the DESKTOP default set (four "a person must act"
// events), which is narrower than the webhook default set.
func TestMatchDesktopDefaultEvents(t *testing.T) {
	cfg := desktopCfg(nil, nil)
	for _, ev := range []string{"job.awaiting_approval", "interaction.created", "session.awaiting_reply", "job.needs_review"} {
		if !MatchDesktop(cfg, ev, "p") {
			t.Errorf("MatchDesktop(%s) = false, want true (default desktop event)", ev)
		}
	}
	for _, ev := range []string{"job.terminal", "work.digest", "job.running"} {
		if MatchDesktop(cfg, ev, "p") {
			t.Errorf("MatchDesktop(%s) = true, want false (not a desktop default)", ev)
		}
	}
}

func TestMatchDesktopExplicitEvents(t *testing.T) {
	cfg := desktopCfg([]string{"job.terminal"}, nil)
	if !MatchDesktop(cfg, "job.terminal", "p") {
		t.Error("MatchDesktop(job.terminal) = false, want true")
	}
	if MatchDesktop(cfg, "job.needs_review", "p") {
		t.Error("MatchDesktop(job.needs_review) = true, want false (explicit filter replaces the default)")
	}
}

func TestMatchDesktopProjects(t *testing.T) {
	cfg := desktopCfg(nil, []string{"alpha"})
	if !MatchDesktop(cfg, "job.needs_review", "alpha") {
		t.Error("MatchDesktop(alpha) = false, want true")
	}
	if MatchDesktop(cfg, "job.needs_review", "beta") {
		t.Error("MatchDesktop(beta) = true, want false (not in projects)")
	}
}

// Two pause switches, both nil = on: the notification block and the desktop block.
func TestMatchDesktopPauseSwitches(t *testing.T) {
	off := false
	notifOff := desktopCfg(nil, nil)
	notifOff.Enabled = &off
	if MatchDesktop(notifOff, "job.needs_review", "p") {
		t.Error("MatchDesktop = true with notification.enabled false, want false")
	}

	desktopOff := desktopCfg(nil, nil)
	desktopOff.Desktop.Enabled = &off
	if MatchDesktop(desktopOff, "job.needs_review", "p") {
		t.Error("MatchDesktop = true with notification.desktop.enabled false, want false")
	}
}

// No desktop block means the channel is off — the very reason an upgraded server
// does not start popping windows.
func TestMatchDesktopAbsentBlock(t *testing.T) {
	if MatchDesktop(nil, "job.needs_review", "p") {
		t.Error("MatchDesktop(nil) = true, want false")
	}
	if MatchDesktop(&config.NotificationConfig{}, "job.needs_review", "p") {
		t.Error("MatchDesktop(no desktop block) = true, want false")
	}
}
