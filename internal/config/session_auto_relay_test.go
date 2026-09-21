package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAutoRelayIdleSecUnsetDefaults verifies the SR-A5 threshold defaults to 5
// minutes when the key is absent — auto-arming ships ON, so an existing config
// gets it without edits.
func TestAutoRelayIdleSecUnsetDefaults(t *testing.T) {
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
	if got := cfg.EffectiveAutoRelayIdleSec(); got != DefaultSessionAutoRelayIdleSec {
		t.Fatalf("unset threshold = %d, want the %d default", got, DefaultSessionAutoRelayIdleSec)
	}
	var zero *Config
	if zero.EffectiveAutoRelayIdleSec() != DefaultSessionAutoRelayIdleSec {
		t.Fatal("a nil config should report the default threshold")
	}
}

// TestLoadRejectsSessionAutoRelayIdleSecAlias: the pre-R2
// `server.session_auto_relay_idle_sec` alias is GONE (v0.48 / G032). A config that
// still writes it must fail the load — naming the key to rewrite — instead of
// silently dropping the threshold back to the 5-minute default.
func TestLoadRejectsSessionAutoRelayIdleSecAlias(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
server:
  session_auto_relay_idle_sec: 90
projects:
  demo:
    host_path: /tmp/demo
`)
	_, _, err := Load(p)
	if err == nil {
		t.Fatal("Load accepted the removed server.session_auto_relay_idle_sec alias")
	}
	if !strings.Contains(err.Error(), "session_auto_relay_idle_sec has been removed; use session.auto_relay_idle_sec") {
		t.Fatalf("error = %v, want it to name the removed key and its replacement", err)
	}
}

// TestSessionConfigBlock pins the session block itself (R2): the two thresholds
// live there (defaults 300 / 900) and an explicit `0` is an OFF switch for that
// criterion, never "use the default".
func TestSessionConfigBlock(t *testing.T) {
	load := func(t *testing.T, body string) *Config {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "cfg.yaml")
		write(t, p, body)
		cfg, _, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return cfg
	}
	const projects = `
projects:
  demo:
    host_path: /tmp/demo
`

	// Unset → the shipped defaults, both criteria on.
	cfg := load(t, projects)
	if got := cfg.EffectiveAutoRelayIdleSec(); got != DefaultSessionAutoRelayIdleSec {
		t.Fatalf("idle default = %d, want %d", got, DefaultSessionAutoRelayIdleSec)
	}
	if got := cfg.EffectiveAutoRelayTurnSec(); got != DefaultSessionAutoRelayTurnSec {
		t.Fatalf("turn default = %d, want %d", got, DefaultSessionAutoRelayTurnSec)
	}

	// The new block, both keys.
	cfg = load(t, `
session:
  auto_relay_idle_sec: 120
  auto_relay_turn_sec: 600
`+projects)
	if got := cfg.EffectiveAutoRelayIdleSec(); got != 120 {
		t.Fatalf("session block idle = %d, want 120", got)
	}
	if got := cfg.EffectiveAutoRelayTurnSec(); got != 600 {
		t.Fatalf("session block turn = %d, want 600", got)
	}

	// Explicit zeros disable one criterion each — never "use the default".
	cfg = load(t, `
session:
  auto_relay_idle_sec: 0
  auto_relay_turn_sec: 0
`+projects)
	if cfg.EffectiveAutoRelayIdleSec() != 0 || cfg.EffectiveAutoRelayTurnSec() != 0 {
		t.Fatalf("zeros = %d/%d, want 0/0", cfg.EffectiveAutoRelayIdleSec(), cfg.EffectiveAutoRelayTurnSec())
	}

	// One key written, the other unset → the written one wins and the other keeps
	// its default (the two criteria are independent).
	cfg = load(t, `
session:
  auto_relay_idle_sec: 90
`+projects)
	if got := cfg.EffectiveAutoRelayIdleSec(); got != 90 {
		t.Fatalf("idle = %d, want 90", got)
	}
	if got := cfg.EffectiveAutoRelayTurnSec(); got != DefaultSessionAutoRelayTurnSec {
		t.Fatalf("turn must keep its default when only idle is written, got %d", got)
	}
}

// TestSessionSupervisingDefaults pins the supervision gate's configuration
// (SUP-01 D): skipping the auto-arm while the caller has live jobs is ON unless
// the operator turns it off (a config that predates the key gets the fix), the
// window defaults to two hours, and an explicit `0` window is a valid "no window"
// rather than "unset".
func TestSessionSupervisingDefaults(t *testing.T) {
	projects := `
projects:
  demo:
    host_path: /tmp/demo
`
	load := func(t *testing.T, body string) *Config {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "cfg.yaml")
		write(t, p, body)
		cfg, _, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return cfg
	}

	cfg := load(t, projects)
	if !cfg.EffectiveAutoRelaySkipWhenSupervising() {
		t.Fatal("skip-while-supervising must default to ON")
	}
	if got := cfg.EffectiveSessionSupervisingWindowSec(); got != DefaultSessionSupervisingWindowSec {
		t.Fatalf("window default = %d, want %d", got, DefaultSessionSupervisingWindowSec)
	}

	cfg = load(t, `
session:
  auto_relay_skip_when_supervising: false
  supervising_window_sec: 600
`+projects)
	if cfg.EffectiveAutoRelaySkipWhenSupervising() {
		t.Fatal("explicit false must disable the gate")
	}
	if got := cfg.EffectiveSessionSupervisingWindowSec(); got != 600 {
		t.Fatalf("window = %d, want 600", got)
	}

	cfg = load(t, `
session:
  supervising_window_sec: 0
`+projects)
	if got := cfg.EffectiveSessionSupervisingWindowSec(); got != 0 {
		t.Fatalf("explicit 0 window = %d, want 0 (every live job counts)", got)
	}
}
