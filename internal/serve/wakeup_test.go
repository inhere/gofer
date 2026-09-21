package serve

import (
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/testutil/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestScheduleLoopFiresDueWakeups: the JOB-09 half of the schedule tick. One pass
// over the store's due rows must (a) advance each timer with a compare-and-swap
// before firing it — so an overlapping tick cannot double-fire — (b) retire an
// expired wakeup instead of firing it, and (c) leave a row that is not due alone.
// It runs against a REAL store (the query and the CAS are the parts the sweep
// depends on) with the fire/disable/expire callbacks observed.
func TestScheduleLoopFiresDueWakeups(t *testing.T) {
	meta, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })

	const now = int64(1_800_000_000)
	insert := func(r jobstore.WakeupRecord) {
		t.Helper()
		if err := meta.InsertWakeup(r); err != nil {
			t.Fatalf("InsertWakeup(%s): %v", r.ID, err)
		}
	}
	insert(jobstore.WakeupRecord{
		ID: "wk-at", JobID: "job-1", Kind: jobstore.WakeupKindAt, At: now - 5,
		Mode: jobstore.WakeupModeOnce, Enabled: 1, NextRunAt: now - 5, CreatedAt: 1,
		ExpiresAt: now + 3600,
	})
	insert(jobstore.WakeupRecord{
		ID: "wk-every", JobID: "job-1", Kind: jobstore.WakeupKindEvery, EverySec: 600,
		Mode: jobstore.WakeupModeContinuous, Enabled: 1, NextRunAt: now - 100, CreatedAt: 2,
		ExpiresAt: now + 3600,
	})
	insert(jobstore.WakeupRecord{
		ID: "wk-later", JobID: "job-1", Kind: jobstore.WakeupKindAt, At: now + 600,
		Mode: jobstore.WakeupModeOnce, Enabled: 1, NextRunAt: now + 600, CreatedAt: 3,
		ExpiresAt: now + 3600,
	})
	insert(jobstore.WakeupRecord{
		ID: "wk-expired", JobID: "job-1", Kind: jobstore.WakeupKindEvent,
		EventTypesJSON: jobstore.EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "job-1", Mode: jobstore.WakeupModeOnce, Enabled: 1, CreatedAt: 4,
		ExpiresAt: now - 1,
	})

	due, err := meta.DueWakeups(now)
	if err != nil {
		t.Fatalf("DueWakeups: %v", err)
	}
	if len(due) != 3 {
		t.Fatalf("due = %+v, want the two timers and the expired row (not the future one)", due)
	}

	var fired, disabled, expired []string
	sweepDueWakeups(now, due,
		func(w jobstore.WakeupRecord, after int64) (int64, error) {
			return job.WakeupNextRun(w, after)
		},
		func(id string, oldNext, newNext int64) (bool, error) {
			return meta.AdvanceWakeup(id, oldNext, newNext, now)
		},
		func(w jobstore.WakeupRecord, reason string) { fired = append(fired, w.ID+":"+reason) },
		func(id string) { disabled = append(disabled, id) },
		func(id string) { expired = append(expired, id) },
		func(string, ...any) {}, func(string, ...any) {})

	assert.Eq(t, []string{"wk-expired"}, expired)
	// The pass fires in due order (the earliest instant first); the fire COUNTERS are
	// the job service's claim, not the sweep's, so they are asserted in internal/job.
	assert.Eq(t, []string{"wk-every:every", "wk-at:at"}, fired)
	// `at` never repeats and is consumed; `every` stays armed and is only advanced.
	assert.Eq(t, []string{"wk-at"}, disabled)

	// The store really moved: the one-shot is consumed, the every-timer counts from
	// the sweep instant (never replaying the tick it missed), and the future row is
	// untouched.
	at, _, _ := meta.GetWakeup("wk-at")
	assert.Eq(t, int64(0), at.NextRunAt)
	every, _, _ := meta.GetWakeup("wk-every")
	assert.Eq(t, now+600, every.NextRunAt)
	assert.Eq(t, now, every.LastFiredAt)
	later, _, _ := meta.GetWakeup("wk-later")
	assert.Eq(t, now+600, later.NextRunAt)
	assert.Eq(t, now+600, later.At)

	// A second pass over the SAME rows (an overlapping tick that already read them)
	// must not fire anything: the advance CAS has moved on.
	fired = nil
	sweepDueWakeups(now, due,
		func(w jobstore.WakeupRecord, after int64) (int64, error) { return job.WakeupNextRun(w, after) },
		func(id string, oldNext, newNext int64) (bool, error) {
			return meta.AdvanceWakeup(id, oldNext, newNext, now)
		},
		func(w jobstore.WakeupRecord, reason string) { fired = append(fired, w.ID) },
		func(string) {}, func(string) {},
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, len(fired))
}
