package jobstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func insertHealthSchedule(t *testing.T, s *Store, id string, mutate func(*ScheduleRecord)) {
	t.Helper()
	r := ScheduleRecord{
		ID: id, Name: id, ScheduleType: "cron", CronExpr: "* * * * *", RequestJSON: `{}`,
		Enabled: 1, NextRunAt: 100, Overlap: ScheduleOverlapSkip, MaxFailures: 3, CreatedAt: 1, UpdatedAt: 1,
	}
	if mutate != nil {
		mutate(&r)
	}
	assert.NoErr(t, s.InsertSchedule(r))
}

func scheduleJob(id, scheduleID, status string) JobRecord {
	r := sampleJob(id, "proj", 100)
	r.Status = status
	r.ScheduleID = scheduleID
	r.ScheduleTrigger = ScheduleTriggerTimer
	return r
}

// The statuses the overlap check treats as unfinished are part of the contract
// (design 2026-11-12 §1): pin them literally.
func TestScheduleActiveStatuses(t *testing.T) {
	assert.Eq(t, []string{
		"queued", "running", "recovering", "waiting_dir", "awaiting_input",
		"awaiting_approval", "pending_interaction", "paused_for_update",
	}, ScheduleActiveStatuses())
}

func TestScheduleHasActiveJob(t *testing.T) {
	s := openTest(t)
	insertHealthSchedule(t, s, "sch-a", nil)

	has, err := s.ScheduleHasActiveJob("sch-a")
	assert.NoErr(t, err)
	assert.False(t, has) // no job at all (also: every job pruned)

	for i, st := range ScheduleActiveStatuses() {
		id := "j-" + st
		assert.NoErr(t, s.UpsertJob(scheduleJob(id, "sch-a", st)))
		has, err = s.ScheduleHasActiveJob("sch-a")
		assert.NoErr(t, err)
		assert.True(t, has, "status %s (#%d) must count as unfinished", st, i)
		_, err = s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
		assert.NoErr(t, err)
	}

	// Finished jobs do not count; needs_review is finished work awaiting acceptance.
	for _, st := range []string{"done", "failed", "cancelled", "timeout", "rejected", "needs_review"} {
		assert.NoErr(t, s.UpsertJob(scheduleJob("f-"+st, "sch-a", st)))
	}
	has, err = s.ScheduleHasActiveJob("sch-a")
	assert.NoErr(t, err)
	assert.False(t, has)

	// An unfinished job of ANOTHER schedule does not count either.
	assert.NoErr(t, s.UpsertJob(scheduleJob("other", "sch-b", "running")))
	has, err = s.ScheduleHasActiveJob("sch-a")
	assert.NoErr(t, err)
	assert.False(t, has)
	id, err := s.ScheduleActiveJobID("sch-b")
	assert.NoErr(t, err)
	assert.Eq(t, "other", id)
}

func TestScheduleIDRoundTripsOnJobRow(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertJob(scheduleJob("rt", "sch-x", "done")))
	got, ok, err := s.GetJob("rt")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "sch-x", got.ScheduleID)
	assert.Eq(t, ScheduleTriggerTimer, got.ScheduleTrigger)
	plain := sampleJob("plain", "proj", 100)
	assert.NoErr(t, s.UpsertJob(plain))
	got, _, _ = s.GetJob("plain")
	assert.Eq(t, "", got.ScheduleID)
}

// run_count / consecutive_failures are single-statement updates: concurrent increments
// never lose one, and each caller sees a distinct returned value.
func TestIncScheduleCountersAtomic(t *testing.T) {
	s := openTest(t)
	insertHealthSchedule(t, s, "sch-c", func(r *ScheduleRecord) { r.MaxRuns = 100; r.MaxFailures = 100 })
	const n = 40
	runs := make(chan int, n)
	fails := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, ok, err := s.IncScheduleRun("sch-c")
			if err != nil || !ok {
				t.Errorf("IncScheduleRun ok=%v err=%v", ok, err)
				return
			}
			runs <- c
			f, _, ok, err := s.IncScheduleFailure("sch-c")
			if err != nil || !ok {
				t.Errorf("IncScheduleFailure ok=%v err=%v", ok, err)
				return
			}
			fails <- f
		}()
	}
	wg.Wait()
	close(runs)
	close(fails)
	seenRun, seenFail := map[int]bool{}, map[int]bool{}
	for c := range runs {
		assert.False(t, seenRun[c], "duplicate run count %d", c)
		seenRun[c] = true
	}
	for f := range fails {
		assert.False(t, seenFail[f], "duplicate failure count %d", f)
		seenFail[f] = true
	}
	got, _, _ := s.GetSchedule("sch-c")
	assert.Eq(t, n, got.RunCount)
	assert.Eq(t, n, got.ConsecutiveFailures)

	assert.NoErr(t, s.ResetScheduleFailures("sch-c"))
	got, _, _ = s.GetSchedule("sch-c")
	assert.Eq(t, 0, got.ConsecutiveFailures)
	assert.Eq(t, n, got.RunCount) // a reset never touches the run count
}

func TestIncScheduleIgnoresOnceAndUnknown(t *testing.T) {
	s := openTest(t)
	insertHealthSchedule(t, s, "sch-once", func(r *ScheduleRecord) { r.ScheduleType = "once"; r.CronExpr = "" })
	_, _, ok, err := s.IncScheduleRun("sch-once")
	assert.NoErr(t, err)
	assert.False(t, ok)
	_, _, ok, err = s.IncScheduleFailure("sch-once")
	assert.NoErr(t, err)
	assert.False(t, ok)
	_, _, ok, err = s.IncScheduleFailure("nope")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// PauseSchedule flips an enabled schedule exactly once, so the pause is announced once.
func TestPauseScheduleOnce(t *testing.T) {
	s := openTest(t)
	insertHealthSchedule(t, s, "sch-p", nil)
	first, err := s.PauseSchedule("sch-p", SchedulePausedFailures)
	assert.NoErr(t, err)
	assert.True(t, first)
	second, err := s.PauseSchedule("sch-p", SchedulePausedMaxRuns)
	assert.NoErr(t, err)
	assert.False(t, second)
	got, _, _ := s.GetSchedule("sch-p")
	assert.Eq(t, 0, got.Enabled)
	assert.Eq(t, SchedulePausedFailures, got.PausedReason) // the first reason stands
}

// Switching a schedule on by hand clears the failure streak and the pause reason but
// keeps run_count; switching it off by hand is not an automatic pause.
func TestSetScheduleEnabledClearsPause(t *testing.T) {
	s := openTest(t)
	insertHealthSchedule(t, s, "sch-e", func(r *ScheduleRecord) { r.RunCount = 5 })
	_, _, _, _ = s.IncScheduleFailure("sch-e")
	_, _, _, _ = s.IncScheduleFailure("sch-e")
	_, err := s.PauseSchedule("sch-e", SchedulePausedFailures)
	assert.NoErr(t, err)

	assert.NoErr(t, s.SetScheduleEnabled("sch-e", 1))
	got, _, _ := s.GetSchedule("sch-e")
	assert.Eq(t, 1, got.Enabled)
	assert.Eq(t, 0, got.ConsecutiveFailures)
	assert.Eq(t, "", got.PausedReason)
	assert.Eq(t, 5, got.RunCount)

	assert.NoErr(t, s.SetScheduleEnabled("sch-e", 0))
	got, _, _ = s.GetSchedule("sch-e")
	assert.Eq(t, "", got.PausedReason)
}

// The same through PATCH's UpdateSchedule: enabling a paused schedule clears the pause;
// a patch that leaves it disabled keeps the reason.
func TestUpdateScheduleEnableClearsPause(t *testing.T) {
	s := openTest(t)
	insertHealthSchedule(t, s, "sch-u", nil)
	_, _, _, _ = s.IncScheduleFailure("sch-u")
	_, err := s.PauseSchedule("sch-u", SchedulePausedFailures)
	assert.NoErr(t, err)
	cur, _, _ := s.GetSchedule("sch-u")
	upd := ScheduleUpdate{Name: "renamed", ScheduleType: "cron", CronExpr: cur.CronExpr, RequestJSON: cur.RequestJSON,
		Enabled: 0, NextRunAt: cur.NextRunAt, Overlap: "allow", MaxRuns: 7, MaxFailures: 2}
	assert.NoErr(t, s.UpdateSchedule("sch-u", upd))
	got, _, _ := s.GetSchedule("sch-u")
	assert.Eq(t, SchedulePausedFailures, got.PausedReason)
	assert.Eq(t, "allow", got.Overlap)
	assert.Eq(t, 7, got.MaxRuns)
	assert.Eq(t, 2, got.MaxFailures)

	upd.Enabled = 1
	assert.NoErr(t, s.UpdateSchedule("sch-u", upd))
	got, _, _ = s.GetSchedule("sch-u")
	assert.Eq(t, 1, got.Enabled)
	assert.Eq(t, "", got.PausedReason)
	assert.Eq(t, 0, got.ConsecutiveFailures)
}

// A schedule row written by the previous release keeps its behaviour after the
// upgrade: overlap=allow, no failure pause, no run limit (design 2026-11-12 §1).
func TestUpgradedScheduleRowKeepsOldBehavior(t *testing.T) {
	ddl, err := os.ReadFile(filepath.Join("testdata", "schema-v0.213.0.sql"))
	assert.NoErr(t, err)
	path := filepath.Join(t.TempDir(), "gofer.db")
	raw, err := sql.Open("sqlite", path)
	assert.NoErr(t, err)
	_, err = raw.Exec(string(ddl))
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO schedules (id, name, schedule_type, cron_expr, request_json, enabled, next_run_at, created_at, updated_at)
  VALUES ('old', 'old', 'cron', '* * * * *', '{}', 1, 100, 1, 1)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()
	got, ok, err := s.GetSchedule("old")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, ScheduleOverlapAllow, got.Overlap)
	assert.Eq(t, 0, got.MaxFailures)
	assert.Eq(t, 0, got.MaxRuns)
	assert.Eq(t, 0, got.RunCount)
	assert.Eq(t, "", got.PausedReason)
}
