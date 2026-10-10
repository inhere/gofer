package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestEffectiveHoldTimeoutSec pins the hold-timeout resolution (gofer-9b1b): 0 takes
// the default, a positive value within the ceiling is used verbatim, and a negative
// value or one above the ceiling is refused — never truncated.
func TestEffectiveHoldTimeoutSec(t *testing.T) {
	plain := &Config{}
	custom := &Config{Server: ServerConfig{Hold: HoldConfig{DefaultTimeoutSec: 600, MaxTimeoutSec: 3600}}}
	var nilCfg *Config

	cases := []struct {
		name      string
		cfg       *Config
		requested int
		want      int
		errPart   string
	}{
		{"unset takes the built-in default", plain, 0, DefaultHoldTimeoutSec, ""},
		{"nil config takes the built-in default", nilCfg, 0, DefaultHoldTimeoutSec, ""},
		{"unset takes the configured default", custom, 0, 600, ""},
		{"explicit value is kept", custom, 1200, 1200, ""},
		{"explicit value at the ceiling is kept", custom, 3600, 3600, ""},
		{"built-in ceiling", plain, DefaultHoldMaxTimeoutSec, DefaultHoldMaxTimeoutSec, ""},
		{"negative is refused", custom, -1, 0, ">= 0"},
		{"above the configured ceiling is refused", custom, 3601, 0, "exceeds the maximum 3600s"},
		{"above the built-in ceiling is refused", plain, DefaultHoldMaxTimeoutSec + 1, 0, "exceeds the maximum"},
	}
	for _, tc := range cases {
		got, err := tc.cfg.EffectiveHoldTimeoutSec(tc.requested)
		if tc.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errPart) {
				t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.errPart)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got (%d, %v), want (%d, nil)", tc.name, got, err, tc.want)
		}
	}
	if DefaultHoldTimeoutSec != 86400 || DefaultHoldMaxTimeoutSec != 604800 {
		t.Fatalf("hold defaults drifted: %d / %d", DefaultHoldTimeoutSec, DefaultHoldMaxTimeoutSec)
	}
}

// TestHoldConfigValidation: a negative knob, or a default the ceiling would refuse,
// fails config validation instead of failing every held submit later.
func TestHoldConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		hold    HoldConfig
		errPart string
	}{
		{"zero is the default", HoldConfig{}, ""},
		{"explicit pair", HoldConfig{DefaultTimeoutSec: 60, MaxTimeoutSec: 120}, ""},
		{"negative default", HoldConfig{DefaultTimeoutSec: -1}, "server.hold.default_timeout_sec must be >= 0"},
		{"negative max", HoldConfig{MaxTimeoutSec: -5}, "server.hold.max_timeout_sec must be >= 0"},
		{"default above max", HoldConfig{DefaultTimeoutSec: 200, MaxTimeoutSec: 100}, "must not exceed"},
		{"built-in default above a small max", HoldConfig{MaxTimeoutSec: 3600}, "must not exceed"},
	}
	for _, tc := range cases {
		err := validate(&Config{Server: ServerConfig{Hold: tc.hold}})
		if tc.errPart == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.errPart) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.errPart)
		}
	}
}

// TestHoldConfigYAML: the block's yaml spelling is server.hold.{default,max}_timeout_sec.
func TestHoldConfigYAML(t *testing.T) {
	var cfg Config
	src := "server:\n  hold:\n    default_timeout_sec: 120\n    max_timeout_sec: 240\n"
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Server.Hold != (HoldConfig{DefaultTimeoutSec: 120, MaxTimeoutSec: 240}) {
		t.Fatalf("hold = %+v", cfg.Server.Hold)
	}
}
