package config

import (
	"testing"
	"time"
)

func TestStewardConfigDefaults(t *testing.T) {
	var s StewardConfig
	if s.Enabled || s.AgentName() != "" {
		t.Fatalf("steward must be off and agent-less by default: %+v", s)
	}
	if s.ProjectKey() != DefaultProjectKey {
		t.Fatalf("project default = %q, want the built-in default project", s.ProjectKey())
	}
	if s.IdleEnd() != 30*time.Minute || s.MaxReviewItems() != 20 || s.EventThrottle() != 30*time.Minute {
		t.Fatalf("defaults wrong: %v %d %v", s.IdleEnd(), s.MaxReviewItems(), s.EventThrottle())
	}
}

func TestStewardReviewClockDefaultsToTenMinutesBeforeDigest(t *testing.T) {
	var s StewardConfig
	if h, m := s.ReviewClock(WorkConfig{}); h != 8 || m != 50 {
		t.Fatalf("default review clock = %02d:%02d, want 08:50 (10 min before the 09:00 digest)", h, m)
	}
	if h, m := s.ReviewClock(WorkConfig{DigestTime: "00:05"}); h != 23 || m != 55 {
		t.Fatalf("review clock must wrap past midnight, got %02d:%02d", h, m)
	}
	s.ReviewTime = "07:30"
	if h, m := s.ReviewClock(WorkConfig{}); h != 7 || m != 30 {
		t.Fatalf("explicit review time ignored: %02d:%02d", h, m)
	}
}

func TestStewardValidate(t *testing.T) {
	cases := map[string]StewardConfig{
		"negative idle":     {IdleEndMin: -1},
		"padded agent":      {Agent: " claude-acp"},
		"bad review time":   {ReviewTime: "25:00"},
		"garbage time":      {ReviewTime: "soon"},
		"negative throttle": {EventThrottleMin: -5},
	}
	for name, s := range cases {
		if err := s.validate(); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
	if err := (StewardConfig{Enabled: true, Agent: "claude-acp", ReviewTime: "08:15"}).validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
