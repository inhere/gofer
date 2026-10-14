package config

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestEffectiveTrackerPrime (gofer-krzy): the default is nearest — a workspace's
// projects share the workspace tracker; repo is the opt-in isolation.
func TestEffectiveTrackerPrime(t *testing.T) {
	assert.Eq(t, TrackerPrimeNearest, (ProjectConfig{}).EffectiveTrackerPrime())
	assert.Eq(t, TrackerPrimeRepo, (ProjectConfig{TrackerPrime: "repo"}).EffectiveTrackerPrime())
	assert.Eq(t, TrackerPrimeNearest, (ProjectConfig{TrackerPrime: " nearest "}).EffectiveTrackerPrime())
	assert.Eq(t, TrackerPrimeOff, (ProjectConfig{TrackerPrime: "OFF"}).EffectiveTrackerPrime())
}

// TestValidateTrackerPrime: an unknown mode fails the load instead of silently
// falling back to the default scoping.
func TestValidateTrackerPrime(t *testing.T) {
	withMode := func(mode string) *Config {
		cfg := &Config{Projects: map[string]ProjectConfig{"self": {HostPath: t.TempDir()}}}
		if mode != "" {
			p := cfg.Projects["self"]
			p.TrackerPrime = mode
			cfg.Projects["self"] = p
		}
		return cfg
	}
	for _, mode := range []string{"", TrackerPrimeRepo, TrackerPrimeNearest, TrackerPrimeOff} {
		if err := validate(withMode(mode)); err != nil {
			t.Fatalf("tracker_prime %q must load: %v", mode, err)
		}
	}
	err := validate(withMode("closest"))
	assert.ErrMsgContains(t, err, "tracker_prime must be repo, nearest, or off")
}
