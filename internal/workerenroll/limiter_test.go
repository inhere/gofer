package workerenroll

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestLimiterRatePerMinute(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewSourceLimiter(func() int { return 3 }, nil)
	for i := 0; i < 3; i++ {
		assert.NoErr(t, l.Allow("a", now))
	}
	assert.True(t, errors.Is(l.Allow("a", now), ErrRateLimited))
	assert.NoErr(t, l.Allow("b", now)) // another source is independent
	assert.True(t, errors.Is(l.Allow("a", now.Add(59*time.Second)), ErrRateLimited))
	assert.NoErr(t, l.Allow("a", now.Add(61*time.Second))) // window slid
}

func TestLimiterRateIsReadPerCall(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	n := 1
	l := NewSourceLimiter(func() int { return n }, nil)
	assert.NoErr(t, l.Allow("a", now))
	assert.True(t, errors.Is(l.Allow("a", now), ErrRateLimited))
	n = 5
	assert.NoErr(t, l.Allow("a", now))
}

func TestLimiterBlockUntilExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clk := &fakeClock{t: now}
	l := NewSourceLimiter(func() int { return 3 }, clk.Now)
	l.Block("bad", 10*time.Minute)
	assert.True(t, errors.Is(l.Allow("bad", clk.Now()), ErrSourceBlocked))
	assert.True(t, errors.Is(l.Allow("bad", now.Add(9*time.Minute)), ErrSourceBlocked))
	assert.NoErr(t, l.Allow("bad", now.Add(10*time.Minute+time.Second)))
	assert.NoErr(t, l.Allow("good", now))
	l.Block("zero", 0) // ignored
	assert.NoErr(t, l.Allow("zero", now))
}

func TestLimiterCapsTableAndKeepsBlocks(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clk := &fakeClock{t: now}
	l := NewSourceLimiter(func() int { return 3 }, clk.Now)
	l.Block("blocked", time.Hour)
	for i := 0; i < MaxLimiterSources+500; i++ {
		assert.NoErr(t, l.Allow(fmt.Sprintf("s%d", i), now))
	}
	assert.Eq(t, MaxLimiterSources, l.Len())
	// A flood of fresh sources must not flush an active block.
	assert.True(t, errors.Is(l.Allow("blocked", now), ErrSourceBlocked))
}

func TestLimiterTTLEvictsIdleEntries(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewSourceLimiter(func() int { return 3 }, nil)
	for i := 0; i < 50; i++ {
		assert.NoErr(t, l.Allow(fmt.Sprintf("s%d", i), now))
	}
	assert.Eq(t, 50, l.Len())
	assert.NoErr(t, l.Allow("late", now.Add(3*time.Minute)))
	assert.Eq(t, 1, l.Len())
}
