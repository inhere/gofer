package config

import "testing"

// TestProjectVerifyDefaults pins the SUP-01 B verify defaults: a project may
// declare a default verify argv plus its own timeout, an unset timeout falls back
// to 600s, and the project default is carried verbatim (whether it applies is a
// submit-time decision — --no-verify turns it off there, not here).
func TestProjectVerifyDefaults(t *testing.T) {
	if DefaultVerifyTimeoutSec != 600 {
		t.Fatalf("DefaultVerifyTimeoutSec = %d, want 600", DefaultVerifyTimeoutSec)
	}
	cfg := &Config{Projects: map[string]ProjectConfig{
		"plain": {},
		"ver":   {Verify: []string{"go", "test", "./..."}},
		"short": {Verify: []string{"bin", "check"}, VerifyTimeoutSec: 30},
	}}

	if got := cfg.EffectiveVerifyTimeoutSec("ver"); got != DefaultVerifyTimeoutSec {
		t.Errorf("unset verify_timeout_sec resolved to %d, want the %d default", got, DefaultVerifyTimeoutSec)
	}
	if got := cfg.EffectiveVerifyTimeoutSec("short"); got != 30 {
		t.Errorf("explicit verify_timeout_sec resolved to %d, want 30", got)
	}
	// A key the config does not define (a worker-only project, or a request the host
	// only forwards) must still resolve the documented default: 0 would make the
	// verify step die instantly instead of running.
	if got := cfg.EffectiveVerifyTimeoutSec("unknown"); got != DefaultVerifyTimeoutSec {
		t.Errorf("unknown project verify_timeout_sec resolved to %d, want %d", got, DefaultVerifyTimeoutSec)
	}

	if got := cfg.Projects["ver"].Verify; len(got) != 3 || got[0] != "go" || got[2] != "./..." {
		t.Errorf("project verify argv = %v, want it carried verbatim", got)
	}
	if got := cfg.Projects["plain"].Verify; len(got) != 0 {
		t.Errorf("a project without a verify default must have none, got %v", got)
	}
}
