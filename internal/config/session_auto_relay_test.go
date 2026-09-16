package config

import (
	"path/filepath"
	"testing"
)

// TestSessionAutoRelayIdleSecUnsetDefaults verifies the SR-A5 threshold defaults
// to 5 minutes when the key is absent — auto-arming ships ON, so an existing
// config gets it without edits.
func TestSessionAutoRelayIdleSecUnsetDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
projects:
  demo:
    host_path: /tmp/demo
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Server.EffectiveSessionAutoRelayIdleSec(); got != DefaultSessionAutoRelayIdleSec {
		t.Fatalf("unset threshold = %d, want the %d default", got, DefaultSessionAutoRelayIdleSec)
	}
	zero := ServerConfig{}
	if zero.EffectiveSessionAutoRelayIdleSec() != DefaultSessionAutoRelayIdleSec {
		t.Fatal("zero-value ServerConfig should report the default threshold")
	}
}

// TestSessionAutoRelayIdleSecExplicitZeroDisables verifies `0` is an OFF switch,
// not "use the default": the whole point of the pointer is that unset and zero
// differ, and an operator disabling auto-arming must not get it back.
func TestSessionAutoRelayIdleSecExplicitZeroDisables(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
server:
  session_auto_relay_idle_sec: 0
projects:
  demo:
    host_path: /tmp/demo
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Server.EffectiveSessionAutoRelayIdleSec(); got != 0 {
		t.Fatalf("explicit 0 = %d, want 0 (disabled)", got)
	}
}

// TestSessionAutoRelayIdleSecExplicitValue verifies a configured threshold wins.
func TestSessionAutoRelayIdleSecExplicitValue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
server:
  session_auto_relay_idle_sec: 90
projects:
  demo:
    host_path: /tmp/demo
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Server.EffectiveSessionAutoRelayIdleSec(); got != 90 {
		t.Fatalf("threshold = %d, want 90", got)
	}
}
