package acp

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errTurnTimeout is the cancellation CAUSE a turnTimer leaves on the turn's context when
// the turn's running time is used up. It is not context.DeadlineExceeded on purpose: the
// deadline is not the parent's (that is the session's), it is the turn's own clock, which
// stops while the runner waits for a human. turnTimer.TimedOut tells the caller.
var errTurnTimeout = errors.New("acp: turn timed out")

// turnTimer bounds ONE resident turn's RUNNING time (acp.turn_timeout_sec).
//
// Plain context.WithTimeout cannot express this rule (gofer-ji7m): a turn that asks a
// human for approval once — let alone three times — spends most of its wall clock inside
// a permission interaction, and charging that wait to the agent turned a healthy session
// into "turn timed out". The wait for a human is bounded on its own (approval
// timeout_sec + on_timeout), so the turn's budget must only run while the agent is
// actually working: Pause before the approval wait, Resume after the answer.
//
// The budget covers the turn only. The session's own cap (max_session_timeout) keeps
// running through an approval wait — a resident session that nobody answers must still
// end eventually, and its cap is wall clock by definition.
type turnTimer struct {
	budget time.Duration
	cancel context.CancelCauseFunc
	timer  *time.Timer

	// mu guards the clock state: the timer callback fires on its own goroutine, the
	// runner pauses/resumes on its own, and the ACP reader goroutine may also reach a
	// permission request (the approval gate) — every one of them touches this state.
	mu sync.Mutex
	// used is the running time already consumed by this turn (across pauses), started is
	// when the current running stretch began (zero while paused), fired records that the
	// budget ran out, stopped that the turn is over.
	used    time.Duration
	started time.Time
	fired   bool
	stopped bool
}

// newTurnTimer starts a turn's clock under parent and returns the context the prompt runs
// under: it is cancelled with errTurnTimeout when the RUNNING budget is used up, or with
// the parent's cause when the session/job ends.
func newTurnTimer(parent context.Context, budget time.Duration) (context.Context, *turnTimer) {
	ctx, cancel := context.WithCancelCause(parent)
	t := &turnTimer{budget: budget, cancel: cancel, started: time.Now()}
	t.timer = time.AfterFunc(budget, t.expire)
	return ctx, t
}

// expire is the timer callback: the turn's running budget is gone.
func (t *turnTimer) expire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired || t.started.IsZero() {
		return
	}
	t.used = t.budget
	t.started = time.Time{}
	t.fired = true
	t.cancel(errTurnTimeout)
}

// Pause stops the clock and reports the running time used so far and what is left. It is
// a no-op on a turn that is over or has already timed out (the caller still gets the
// snapshot, so the event it records is truthful).
func (t *turnTimer) Pause() (used, remaining time.Duration) {
	if t == nil {
		return 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started.IsZero() && !t.stopped && !t.fired {
		t.used += time.Since(t.started)
		t.started = time.Time{}
		t.timer.Stop()
	}
	return t.used, t.remainingLocked()
}

// Resume restarts the clock after a pause and reports the same snapshot as Pause. A budget
// already spent resumes straight into the timeout (the answer to the wait came too late).
func (t *turnTimer) Resume() (used, remaining time.Duration) {
	if t == nil {
		return 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired || !t.started.IsZero() {
		return t.used, t.remainingLocked()
	}
	remaining = t.remainingLocked()
	if remaining <= 0 {
		t.used = t.budget
		t.fired = true
		t.cancel(errTurnTimeout)
		return t.used, 0
	}
	t.started = time.Now()
	t.timer.Reset(remaining)
	return t.used, remaining
}

// TimedOut reports whether the turn's running budget ran out (as opposed to the session or
// the job ending underneath it).
func (t *turnTimer) TimedOut() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fired
}

// Stop ends the clock: no further expiry, and the turn's context is cancelled so the ACP
// client unblocks its prompt read.
func (t *turnTimer) Stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.stopped = true
	t.timer.Stop()
	t.cancel(nil)
}

// remainingLocked is the budget left from the last paused/running state (0 once spent).
func (t *turnTimer) remainingLocked() time.Duration {
	if t.fired || t.used >= t.budget {
		return 0
	}
	return t.budget - t.used
}

// Budget is the turn's total running budget, for the events the gate records.
func (t *turnTimer) Budget() time.Duration {
	if t == nil {
		return 0
	}
	return t.budget
}
