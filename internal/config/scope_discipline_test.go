package config

import (
	"strings"
	"testing"
)

// TestScopeDisciplineValidateAndDefault (gofer-3nxa.3): unset reads as auto, the three
// modes (any case) validate, anything else is refused at load.
func TestScopeDisciplineValidateAndDefault(t *testing.T) {
	if got := (ProjectConfig{}).EffectiveScopeDiscipline(); got != ScopeDisciplineAuto {
		t.Fatalf("unset = %q, want auto", got)
	}
	if got := (ProjectConfig{ScopeDiscipline: " OFF "}).EffectiveScopeDiscipline(); got != ScopeDisciplineOff {
		t.Fatalf("OFF = %q, want off", got)
	}
	for _, mode := range []string{"", "auto", "on", "off", "On"} {
		cfg := &Config{Projects: map[string]ProjectConfig{"p": {HostPath: "/x", ScopeDiscipline: mode}}}
		if err := Validate(cfg); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
	cfg := &Config{Projects: map[string]ProjectConfig{"p": {HostPath: "/x", ScopeDiscipline: "always"}}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "scope_discipline") {
		t.Fatalf("bad mode err = %v", err)
	}
}
