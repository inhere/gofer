package config

import (
	"errors"
	"testing"
	"time"
)

// TestEffectiveAuditRetentionDays: 0 / unset takes the default, a positive value is kept,
// a negative value stays negative (keep forever).
func TestEffectiveAuditRetentionDays(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, DefaultAuditRetentionDays}, {30, 30}, {-1, -1}} {
		if got := (ServerConfig{AuditRetentionDays: tc.in}).EffectiveAuditRetentionDays(); got != tc.want {
			t.Errorf("AuditRetentionDays=%d -> %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestParseCallerExpiry: 90d / a duration / a date (end of day, UTC) / RFC 3339 / never, and
// the refusals.
func TestParseCallerExpiry(t *testing.T) {
	now := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"", 0}, {"never", 0}, {"NEVER", 0},
		{"90d", now.Add(90 * 24 * time.Hour).Unix()},
		{"36h", now.Add(36 * time.Hour).Unix()},
		{"2026-12-31", time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC).Unix()},
		{"2027-01-01T00:00:00Z", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).Unix()},
	} {
		got, err := ParseCallerExpiry(tc.in, now)
		if err != nil || got != tc.want {
			t.Errorf("ParseCallerExpiry(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"soon", "0d", "-1d", "xd", "-5h", "2026-11-30", "2020-01-01T00:00:00Z"} {
		if _, err := ParseCallerExpiry(bad, now); !errors.Is(err, ErrCallerInvalid) {
			t.Errorf("ParseCallerExpiry(%q) = %v, want ErrCallerInvalid", bad, err)
		}
	}
}
