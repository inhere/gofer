package serve

import (
	"testing"
	"time"
)

// TestSweepClockAdvances pins the stamp the health self-check's sweep probe reads: the
// zero value means "no completed pass yet" and every Mark moves the stamp forward.
func TestSweepClockAdvances(t *testing.T) {
	clock := &sweepClock{}
	if !clock.LastSweepAt().IsZero() {
		t.Fatalf("fresh clock LastSweepAt = %v, want zero", clock.LastSweepAt())
	}
	first := time.Unix(1000, 0)
	clock.Mark(first)
	if got := clock.LastSweepAt(); !got.Equal(first) {
		t.Fatalf("after first pass LastSweepAt = %v, want %v", got, first)
	}
	second := first.Add(30 * time.Second)
	clock.Mark(second)
	if got := clock.LastSweepAt(); !got.Equal(second) {
		t.Fatalf("after second pass LastSweepAt = %v, want %v", got, second)
	}
}
