package config

import (
	"path/filepath"
	"testing"
)

// TestSessionStopJobWait pins session.stop_job_wait (gofer-rhux): the three spellings
// round-trip, and an unset OR unrecognised value means `away` — the old "block on every
// pending job" must not come back through a typo.
func TestSessionStopJobWait(t *testing.T) {
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
	base := "projects:\n  demo:\n    host_path: /tmp/demo\n"

	cfg := load(t, base)
	if got := cfg.EffectiveSessionStopJobWait(); got != StopJobWaitAway {
		t.Fatalf("unset stop_job_wait = %q, want %q", got, StopJobWaitAway)
	}
	if got := cfg.ConfiguredSessionStopJobWait(); got != "" {
		t.Fatalf("unset configured value = %q, want empty (the server decides)", got)
	}

	for _, tc := range []struct {
		written  string
		want     string
		wantConf string
	}{
		{written: "away", want: StopJobWaitAway, wantConf: StopJobWaitAway},
		{written: "always", want: StopJobWaitAlways, wantConf: StopJobWaitAlways},
		{written: "never", want: StopJobWaitNever, wantConf: StopJobWaitNever},
		// A typo is not a value: it must fall back to `away`, never to the blocking one.
		{written: "sometimes", want: StopJobWaitAway, wantConf: "sometimes"},
	} {
		cfg := load(t, base+"session:\n  stop_job_wait: "+tc.written+"\n")
		if got := cfg.EffectiveSessionStopJobWait(); got != tc.want {
			t.Fatalf("stop_job_wait=%s: effective = %q, want %q", tc.written, got, tc.want)
		}
		if got := cfg.ConfiguredSessionStopJobWait(); got != tc.wantConf {
			t.Fatalf("stop_job_wait=%s: configured = %q, want %q", tc.written, got, tc.wantConf)
		}
	}

	var zero *Config
	if zero.EffectiveSessionStopJobWait() != StopJobWaitAway || zero.ConfiguredSessionStopJobWait() != "" {
		t.Fatal("a nil config must report the away default and no configured value")
	}

	// The Clone the config write transaction hands out keeps the string (it is a value
	// field, unlike the pointer knobs around it).
	cfg = load(t, base+"session:\n  stop_job_wait: never\n")
	if got := cfg.Clone().EffectiveSessionStopJobWait(); got != StopJobWaitNever {
		t.Fatalf("clone lost the value: %q", got)
	}
}
