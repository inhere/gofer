package steward

import (
	"context"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/settings"
)

// startHeldReview begins a review whose turn never ends on its own, so the test can look at the
// observe gate while it is in flight; the returned func lets it finish.
func startHeldReview(t *testing.T, e *env, trigger string) func() {
	t.Helper()
	e.item(t, "等设备")
	e.host.hold = true
	res, err := e.svc.RunReview(context.Background(), ReviewOpts{Trigger: trigger, Force: true})
	assert.NoErr(t, err)
	return func() {
		e.host.idle(res.JobID)
		e.host.mu.Lock()
		e.host.jobs[res.JobID].info.TurnNo = 2
		e.host.mu.Unlock()
		e.svc.WaitIdle()
	}
}

func TestProactiveReviewOnlyRecords(t *testing.T) {
	for _, trigger := range []string{TriggerDaily, TriggerEvent} {
		t.Run(trigger, func(t *testing.T) {
			e := newEnv(t)
			e.svc.SetSettings(settings.NewService(e.st))
			assert.False(t, e.svc.Observing()) // no review in flight: nothing is held back
			finish := startHeldReview(t, e, trigger)
			assert.True(t, e.svc.Observing())
			assert.True(t, e.svc.Observe("PATCH", "/v1/work-items/w-1", `{"status":"needs_me"}`))
			finish()
			assert.False(t, e.svc.Observing()) // the review ended: writes apply again

			rows, err := e.svc.Observations(10)
			assert.NoErr(t, err)
			assert.Eq(t, 1, len(rows))
			assert.Eq(t, "PATCH", rows[0].Method)
			assert.Eq(t, "/v1/work-items/w-1", rows[0].Path)
			assert.Eq(t, `{"status":"needs_me"}`, rows[0].Body)
			assert.True(t, rows[0].ReviewID > 0)
		})
	}
}

func TestManualReviewAndQuestionsStillAct(t *testing.T) {
	e := newEnv(t)
	e.svc.SetSettings(settings.NewService(e.st))
	finish := startHeldReview(t, e, TriggerManual)
	assert.False(t, e.svc.Observing())
	assert.False(t, e.svc.Observe("PATCH", "/v1/work-items/w-1", "{}")) // not recorded: the caller applies it
	finish()
	rows, err := e.svc.Observations(10)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(rows))
}

func TestActProactivelySwitchLiftsObserveMode(t *testing.T) {
	e := newEnv(t)
	set := settings.NewService(e.st)
	e.svc.SetSettings(set)
	_, _, err := set.Set(settings.KeyStewardActProactively, true, "test")
	assert.NoErr(t, err)
	finish := startHeldReview(t, e, TriggerDaily)
	assert.False(t, e.svc.Observing())
	finish()
}
