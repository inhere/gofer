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

// TestSessionConfigBlockAndLegacyAlias pins the R2 config move: the two
// thresholds live in the top-level `session:` block (defaults 300 / 900), the
// pre-R2 `server.session_auto_relay_idle_sec` key is still READ as an alias for
// the idle one, and when both are written the new block wins.
func TestSessionConfigBlockAndLegacyAlias(t *testing.T) {
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

	// The pre-R2 key keeps working: read as the alias, and carried into the new
	// block by the load-time compat pass.
	cfg = load(t, `
server:
  session_auto_relay_idle_sec: 90
`+projects)
	if got := cfg.EffectiveAutoRelayIdleSec(); got != 90 {
		t.Fatalf("legacy alias = %d, want 90", got)
	}
	if cfg.Session.AutoRelayIdleSec == nil || *cfg.Session.AutoRelayIdleSec != 90 {
		t.Fatalf("legacy key was not carried into the session block: %+v", cfg.Session)
	}
	if got := cfg.EffectiveAutoRelayTurnSec(); got != DefaultSessionAutoRelayTurnSec {
		t.Fatalf("legacy config must keep the turn default, got %d", got)
	}

	// Both keys written → the new block is authoritative.
	cfg = load(t, `
server:
  session_auto_relay_idle_sec: 90
session:
  auto_relay_idle_sec: 120
`+projects)
	if got := cfg.EffectiveAutoRelayIdleSec(); got != 120 {
		t.Fatalf("both keys written: idle = %d, want the new block's 120", got)
	}
}
