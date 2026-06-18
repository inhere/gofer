package config

import (
	"testing"
	"time"
)

func TestRetentionEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  RetentionConfig
		want bool
	}{
		{"zero", RetentionConfig{}, false},
		{"interval only is not enabled", RetentionConfig{IntervalMinutes: 30}, false},
		{"age enables", RetentionConfig{MaxAgeDays: 7}, true},
		{"count enables", RetentionConfig{MaxCount: 100}, true},
	}
	for _, tc := range cases {
		if got := tc.cfg.Enabled(); got != tc.want {
			t.Fatalf("%s: Enabled()=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestRetentionMaxAge(t *testing.T) {
	if got := (RetentionConfig{MaxAgeDays: 7}).MaxAge(); got != 7*24*time.Hour {
		t.Fatalf("MaxAge() = %v, want 168h", got)
	}
	// 0 days -> no age bound (zero duration).
	if got := (RetentionConfig{MaxCount: 10}).MaxAge(); got != 0 {
		t.Fatalf("expected zero MaxAge for 0 days, got %v", got)
	}
}

func TestRetentionPruneInterval(t *testing.T) {
	if got := (RetentionConfig{}).PruneInterval(); got != 60*time.Minute {
		t.Fatalf("default interval = %v, want 60m", got)
	}
	if got := (RetentionConfig{IntervalMinutes: 15}).PruneInterval(); got != 15*time.Minute {
		t.Fatalf("interval = %v, want 15m", got)
	}
}
