package worker

import (
	"math/rand"
	"testing"
	"time"
)

// TestBackoffFullJitterBounds: every backoff sample is in [0, cap] where cap =
// min(max, initial*2^attempt). A deterministic rng makes the test reproducible.
func TestBackoffFullJitterBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	b := newBackoffPolicy(time.Second, 30*time.Second, rng)

	cases := []struct {
		attempt int
		wantCap time.Duration
	}{
		{0, 1 * time.Second},   // initial * 2^0
		{1, 2 * time.Second},   // * 2^1
		{2, 4 * time.Second},   // * 2^2
		{3, 8 * time.Second},   // * 2^3
		{4, 16 * time.Second},  // * 2^4
		{5, 30 * time.Second},  // 32s capped to 30s
		{10, 30 * time.Second}, // saturated at max
		{40, 30 * time.Second}, // no overflow at large attempt
	}
	for _, c := range cases {
		for i := 0; i < 200; i++ {
			d := b.next(c.attempt)
			if d < 0 || d > c.wantCap {
				t.Fatalf("attempt %d: backoff %s out of [0,%s]", c.attempt, d, c.wantCap)
			}
		}
	}
}

// TestBackoffDefaults: zero/negative initial+max fall to the package defaults.
func TestBackoffDefaults(t *testing.T) {
	b := newBackoffPolicy(0, 0, rand.New(rand.NewSource(2)))
	if b.initial != DefaultInitialBackoff || b.max != DefaultMaxBackoff {
		t.Fatalf("defaults = initial %s / max %s, want %s / %s", b.initial, b.max, DefaultInitialBackoff, DefaultMaxBackoff)
	}
	// max < initial is corrected up to initial (never produces a negative cap).
	b2 := newBackoffPolicy(10*time.Second, time.Second, rand.New(rand.NewSource(3)))
	if b2.max < b2.initial {
		t.Fatalf("max %s < initial %s not corrected", b2.max, b2.initial)
	}
}

// TestBackoffNegativeAttempt: a negative attempt is clamped to 0 (no panic).
func TestBackoffNegativeAttempt(t *testing.T) {
	b := newBackoffPolicy(time.Second, 30*time.Second, rand.New(rand.NewSource(4)))
	if d := b.next(-5); d < 0 || d > time.Second {
		t.Fatalf("negative attempt backoff = %s, want [0,1s]", d)
	}
}
