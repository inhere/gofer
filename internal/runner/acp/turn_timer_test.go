package acp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/wait"
)

// TestTurnTimerPausedAcrossWait: the turn's budget does not run while the clock is
// paused, even when the pause is several times longer than the whole budget (gofer-ji7m).
func TestTurnTimerPausedAcrossWait(t *testing.T) {
	const budget = 200 * time.Millisecond
	ctx, clock := newTurnTimer(t.Context(), budget)
	defer clock.Stop()

	used, remaining := clock.Pause()
	if used > 50*time.Millisecond || remaining > budget {
		t.Fatalf("Pause() = used %s remaining %s, want used≈0 and remaining≈%s", used, remaining, budget)
	}
	// The sleep IS the subject here: a wait longer than the whole budget, with the clock
	// stopped, must not consume any of it.
	time.Sleep(3 * budget)
	if clock.TimedOut() {
		t.Fatal("the clock fired while paused")
	}
	if cause := context.Cause(ctx); cause != nil {
		t.Fatalf("context cause while paused = %v, want none", cause)
	}
	used, remaining = clock.Resume()
	if used > 50*time.Millisecond || remaining < budget-50*time.Millisecond {
		t.Fatalf("Resume() = used %s remaining %s, want the whole budget left", used, remaining)
	}
	wait.Until(t, 5*time.Second, "the clock fires after resuming", clock.TimedOut)
	if cause := context.Cause(ctx); !errors.Is(cause, errTurnTimeout) {
		t.Fatalf("context cause after resuming = %v, want errTurnTimeout", cause)
	}
	// A pause/resume on a fired clock is a no-op, not a resurrection.
	clock.Pause()
	if _, remaining := clock.Resume(); remaining != 0 {
		t.Fatalf("remaining after resume on a fired clock = %s, want 0", remaining)
	}
	if !clock.TimedOut() {
		t.Fatal("the clock forgot that it had fired")
	}
}

// TestTurnTimerRunningBudgetExpires: without a pause the budget is spent and the turn's
// context is cancelled with the clock's own cause — not the parent's deadline, so the
// runner can tell a turn timeout from a session/job timeout.
func TestTurnTimerRunningBudgetExpires(t *testing.T) {
	ctx, clock := newTurnTimer(t.Context(), 100*time.Millisecond)
	defer clock.Stop()

	wait.Until(t, 5*time.Second, "the clock fires", clock.TimedOut)
	if cause := context.Cause(ctx); !errors.Is(cause, errTurnTimeout) {
		t.Fatalf("context cause = %v, want errTurnTimeout", cause)
	}
}

// TestTurnTimerStopIsFinal: Stop cancels the turn's context with a plain cancellation (the
// turn ended normally) and never lets the clock fire afterwards.
func TestTurnTimerStopIsFinal(t *testing.T) {
	ctx, clock := newTurnTimer(t.Context(), 100*time.Millisecond)
	clock.Stop()
	wait.Until(t, 5*time.Second, "the context is cancelled", func() bool { return ctx.Err() != nil })
	if clock.TimedOut() {
		t.Fatal("Stop left the clock fired")
	}
	if cause := context.Cause(ctx); errors.Is(cause, errTurnTimeout) {
		t.Fatalf("context cause after Stop = %v, want a plain cancellation", cause)
	}
	time.Sleep(200 * time.Millisecond) // past the budget: a stopped clock must stay quiet
	if clock.TimedOut() {
		t.Fatal("a stopped clock fired")
	}
}
