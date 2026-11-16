package config

// DefaultHealthFailThreshold is how many CONSECUTIVE failed self-checks of one item make
// that item count as failing (design 2026-11-14 §5, Q9): at the 60s cadence about three
// minutes of a real problem, which is long enough that one slow probe never raises a
// card or fails `serve status`.
const DefaultHealthFailThreshold = 3

// HealthConfig is the server.health block. It only tunes the self-check loop's
// reporting; nothing in the job path reads it.
type HealthConfig struct {
	// FailThreshold is the consecutive-failure count that turns a check failing.
	// 0 / unset => DefaultHealthFailThreshold; a negative value is refused by
	// validate (a count, not a duration).
	FailThreshold int `yaml:"fail_threshold,omitempty"`
}

// EffectiveHealthFailThreshold resolves server.health.fail_threshold. A nil config or a
// zero / negative value yields the default, so the loop always has a usable threshold.
func (c *Config) EffectiveHealthFailThreshold() int {
	if c == nil {
		return DefaultHealthFailThreshold
	}
	if n := c.Server.Health.FailThreshold; n > 0 {
		return n
	}
	return DefaultHealthFailThreshold
}
