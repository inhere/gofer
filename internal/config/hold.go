package config

import "fmt"

// Hold-for-approval timeouts (gofer-9b1b). A held job waits in awaiting_approval
// until a human approves or rejects it, or until its hold expires and the expiry
// sweep cancels it. A day is long enough to cover "I am away from the desk"; a week
// is the ceiling a caller may ask for.
const (
	DefaultHoldTimeoutSec    = 86400
	DefaultHoldMaxTimeoutSec = 604800
)

// HoldConfig is the server.hold block. Zero/unset fields take the defaults above.
// Both are read per submit (EffectiveHoldTimeoutSec), so a reload applies to the NEXT
// held job; a job already waiting keeps the deadline fixed when it was submitted.
type HoldConfig struct {
	// DefaultTimeoutSec is the hold timeout of a request that names none
	// (0/unset => DefaultHoldTimeoutSec).
	DefaultTimeoutSec int `yaml:"default_timeout_sec,omitempty"`
	// MaxTimeoutSec is the largest hold timeout a request may ask for
	// (0/unset => DefaultHoldMaxTimeoutSec). A request above it is refused, never
	// silently shortened.
	MaxTimeoutSec int `yaml:"max_timeout_sec,omitempty"`
}

// effectiveDefault / effectiveMax resolve the two knobs to their documented defaults.
func (h HoldConfig) effectiveDefault() int {
	if h.DefaultTimeoutSec > 0 {
		return h.DefaultTimeoutSec
	}
	return DefaultHoldTimeoutSec
}

func (h HoldConfig) effectiveMax() int {
	if h.MaxTimeoutSec > 0 {
		return h.MaxTimeoutSec
	}
	return DefaultHoldMaxTimeoutSec
}

// validate rejects a negative knob and a default the ceiling would refuse.
func (h HoldConfig) validate() error {
	switch {
	case h.DefaultTimeoutSec < 0:
		return fmt.Errorf("server.hold.default_timeout_sec must be >= 0")
	case h.MaxTimeoutSec < 0:
		return fmt.Errorf("server.hold.max_timeout_sec must be >= 0")
	case h.effectiveDefault() > h.effectiveMax():
		return fmt.Errorf("server.hold.default_timeout_sec (%d) must not exceed server.hold.max_timeout_sec (%d)",
			h.effectiveDefault(), h.effectiveMax())
	}
	return nil
}

// EffectiveHoldTimeoutSec resolves a held job's timeout in seconds from the request's
// value: 0 takes server.hold.default_timeout_sec, a positive value is used as is, and
// a negative value or one above server.hold.max_timeout_sec is an error — the request
// is refused rather than truncated, so a caller never waits less than it asked for
// without being told.
func (c *Config) EffectiveHoldTimeoutSec(requested int) (int, error) {
	var h HoldConfig
	if c != nil {
		h = c.Server.Hold
	}
	switch {
	case requested < 0:
		return 0, fmt.Errorf("hold timeout must be >= 0 (0 = the %ds default)", h.effectiveDefault())
	case requested == 0:
		return h.effectiveDefault(), nil
	case requested > h.effectiveMax():
		return 0, fmt.Errorf("hold timeout %ds exceeds the maximum %ds (server.hold.max_timeout_sec)", requested, h.effectiveMax())
	}
	return requested, nil
}
