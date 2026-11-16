package config

import "testing"

// TestEffectiveAuditRetentionDays: 0 / unset takes the default, a positive value is kept,
// a negative value stays negative (keep forever).
func TestEffectiveAuditRetentionDays(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, DefaultAuditRetentionDays}, {30, 30}, {-1, -1}} {
		if got := (ServerConfig{AuditRetentionDays: tc.in}).EffectiveAuditRetentionDays(); got != tc.want {
			t.Errorf("AuditRetentionDays=%d -> %d, want %d", tc.in, got, tc.want)
		}
	}
}
