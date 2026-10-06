package config

import (
	"fmt"
	"strings"
	"time"
)

// StewardConfig is the top-level `steward:` block (W2b, design 2026-10-05 §9 / §14.4).
//
// The steward is a resident ACP session that only schedules and tidies work items: it
// reads, notes, reminds, suggests merges and asks sessions to report, but never decides
// (no done / dropped, no job submit, no config). Everything it needs to carry on lives in
// the database (work items, journal, request ledger, steward notes), so the session is
// disposable and `agent` can be switched at any time.
type StewardConfig struct {
	// Enabled switches the steward on. Off by default: it costs model usage.
	Enabled bool `yaml:"enabled,omitempty"`
	// Agent is the acp-agent that runs the steward session (any installed acp-agent:
	// claude-acp, codex-acp, omp-acp, ...). Empty = not chosen yet (the steward cannot
	// start). Changing it ends the running session; the next need rebuilds it from the
	// prime on the new agent.
	Agent string `yaml:"agent,omitempty"`
	// Project is the project the session runs in (empty = the built-in `default`
	// project, i.e. the server's default workspace).
	Project string `yaml:"project,omitempty"`
	// ReviewTime is the local wall-clock time ("HH:MM") of the daily review. Empty =
	// ReviewLead before the work digest time.
	ReviewTime string `yaml:"review_time,omitempty"`
	// IdleEndMin is how long the session may sit idle before it ends (default 30).
	IdleEndMin int `yaml:"idle_end_min,omitempty"`
	// ReviewMaxItems caps the work items one review handles (default 20).
	ReviewMaxItems int `yaml:"review_max_items,omitempty"`
	// EventWake lets session / due / draft events wake the steward (batched, throttled).
	// Off by default: events are then only noted and handled by the next review.
	EventWake bool `yaml:"event_wake,omitempty"`
	// EventThrottleMin is the minimum gap between two event-driven wakes (default 30).
	EventThrottleMin int `yaml:"event_throttle_min,omitempty"`
}

// W2b steward defaults.
const (
	DefaultStewardIdleEndMin     = 30
	DefaultStewardReviewMaxItems = 20
	DefaultStewardEventThrottle  = 30
	DefaultStewardReviewLeadMin  = 10
	stewardProjectFallback       = DefaultProjectKey
	StewardNotesMaxBytes         = 8 << 10
	StewardPrimeMaxBytes         = 24 << 10
	StewardEventDraftsThreshold  = 5
	stewardReviewTimeLayoutHint  = "HH:MM"
)

// AgentName is the configured steward agent ("" = none chosen).
func (s StewardConfig) AgentName() string { return strings.TrimSpace(s.Agent) }

// ProjectKey is the project the steward session runs in.
func (s StewardConfig) ProjectKey() string {
	if p := strings.TrimSpace(s.Project); p != "" {
		return p
	}
	return stewardProjectFallback
}

// IdleEnd is how long the session may idle before ending.
func (s StewardConfig) IdleEnd() time.Duration {
	return time.Duration(posOr(s.IdleEndMin, DefaultStewardIdleEndMin)) * time.Minute
}

// MaxReviewItems is the per-review item cap.
func (s StewardConfig) MaxReviewItems() int {
	return posOr(s.ReviewMaxItems, DefaultStewardReviewMaxItems)
}

// EventThrottle is the minimum gap between event-driven wakes.
func (s StewardConfig) EventThrottle() time.Duration {
	return time.Duration(posOr(s.EventThrottleMin, DefaultStewardEventThrottle)) * time.Minute
}

// ReviewClock resolves the daily review time of day. An explicit valid review_time
// wins; otherwise it is ReviewLead before the work digest time (so the review's comment
// is ready when the digest goes out); a malformed value falls back to the derived one.
func (s StewardConfig) ReviewClock(w WorkConfig) (hour, minute int) {
	var h, m int
	if n, err := fmt.Sscanf(strings.TrimSpace(s.ReviewTime), "%d:%d", &h, &m); err == nil && n == 2 && h >= 0 && h < 24 && m >= 0 && m < 60 {
		return h, m
	}
	dh, dm := w.DigestClock()
	total := dh*60 + dm - DefaultStewardReviewLeadMin
	if total < 0 {
		total += 24 * 60
	}
	return total / 60, total % 60
}

// validate rejects values that cannot mean anything.
func (s StewardConfig) validate() error {
	for name, v := range map[string]int{
		"steward.idle_end_min": s.IdleEndMin, "steward.review_max_items": s.ReviewMaxItems,
		"steward.event_throttle_min": s.EventThrottleMin,
	} {
		if v < 0 {
			return fmt.Errorf("%s must not be negative (got %d); leave it unset for the default", name, v)
		}
	}
	if a := strings.TrimSpace(s.Agent); a != s.Agent {
		return fmt.Errorf("steward.agent %q has surrounding whitespace", s.Agent)
	}
	if p := strings.TrimSpace(s.Project); p != s.Project {
		return fmt.Errorf("steward.project %q has surrounding whitespace", s.Project)
	}
	if t := strings.TrimSpace(s.ReviewTime); t != "" {
		var h, m int
		if n, err := fmt.Sscanf(t, "%d:%d", &h, &m); err != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
			return fmt.Errorf("steward.review_time %q must look like %s", s.ReviewTime, stewardReviewTimeLayoutHint)
		}
	}
	return nil
}
