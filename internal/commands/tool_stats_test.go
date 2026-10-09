package commands

import (
	"testing"
	"time"
)

func TestParseBackfillSince(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local)
	cases := map[string]int64{
		"":           0,
		"30d":        now.Add(-30 * 24 * time.Hour).Unix(),
		"12h":        now.Add(-12 * time.Hour).Unix(),
		"2026-09-01": time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local).Unix(),
	}
	for in, want := range cases {
		got, err := parseBackfillSince(in, now)
		if err != nil || got != want {
			t.Fatalf("parseBackfillSince(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "-3d", "0d"} {
		if _, err := parseBackfillSince(bad, now); err == nil {
			t.Fatalf("parseBackfillSince(%q) must fail", bad)
		}
	}
}
