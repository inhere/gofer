package serve

import (
	"errors"
	"testing"

	"github.com/gookit/goutil/testutil/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestSweepSchedulesAdvanceSuccessSubmitsAndSetsLast(t *testing.T) {
	var submits, setLast int
	var gotJobID string
	sweepSchedules(120, []jobstore.ScheduleRecord{{ID: "s1", CronExpr: "*/1 * * * *", NextRunAt: 60, CatchUp: 1}}, 3600,
		func(string, int64) (int64, error) { return 180, nil },
		func(id string, oldNext, newNext int64) (bool, error) {
			assert.Eq(t, "s1", id)
			assert.Eq(t, int64(60), oldNext)
			assert.Eq(t, int64(180), newNext)
			return true, nil
		},
		func(r jobstore.ScheduleRecord) (string, error) {
			submits++
			assert.Eq(t, "s1", r.ID)
			return "job-1", nil
		},
		func(id, jobID string) {
			setLast++
			gotJobID = jobID
		},
		func(string, int) {},
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 1, submits)
	assert.Eq(t, 1, setLast)
	assert.Eq(t, "job-1", gotJobID)
}

func TestSweepSchedulesAdvanceFalseSkipsSubmit(t *testing.T) {
	submits := 0
	sweepSchedules(120, []jobstore.ScheduleRecord{{ID: "s1", CronExpr: "*/1 * * * *", NextRunAt: 60, CatchUp: 1}}, 3600,
		func(string, int64) (int64, error) { return 180, nil },
		func(string, int64, int64) (bool, error) { return false, nil },
		func(jobstore.ScheduleRecord) (string, error) {
			submits++
			return "job-1", nil
		},
		func(string, string) {}, func(string, int) {}, func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, submits)
}

func TestSweepSchedulesCatchUpDisabledSkipsMissedRun(t *testing.T) {
	submits, advances := 0, 0
	sweepSchedules(5000, []jobstore.ScheduleRecord{{ID: "s1", CronExpr: "*/1 * * * *", NextRunAt: 60, CatchUp: 0}}, 3600,
		func(string, int64) (int64, error) { return 5040, nil },
		func(string, int64, int64) (bool, error) {
			advances++
			return true, nil
		},
		func(jobstore.ScheduleRecord) (string, error) {
			submits++
			return "job-1", nil
		},
		func(string, string) {}, func(string, int) {}, func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 1, advances)
	assert.Eq(t, 0, submits)
}

func TestSweepSchedulesSubmitErrorDoesNotSetLastOrPanic(t *testing.T) {
	setLast := 0
	sweepSchedules(120, []jobstore.ScheduleRecord{{ID: "s1", CronExpr: "*/1 * * * *", NextRunAt: 60, CatchUp: 1}}, 3600,
		func(string, int64) (int64, error) { return 180, nil },
		func(string, int64, int64) (bool, error) { return true, nil },
		func(jobstore.ScheduleRecord) (string, error) { return "", errors.New("submit failed") },
		func(string, string) { setLast++ },
		func(string, int) {},
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, setLast)
}

func TestSweepSchedulesMultipleDueIndependent(t *testing.T) {
	submitted := make([]string, 0)
	set := make([]string, 0)
	sweepSchedules(120, []jobstore.ScheduleRecord{
		{ID: "ok", CronExpr: "*/1 * * * *", NextRunAt: 60, CatchUp: 1},
		{ID: "stolen", CronExpr: "*/1 * * * *", NextRunAt: 60, CatchUp: 1},
		{ID: "late", CronExpr: "*/1 * * * *", NextRunAt: 1, CatchUp: 0},
		{ID: "fail", CronExpr: "*/1 * * * *", NextRunAt: 60, CatchUp: 1},
	}, 30,
		func(string, int64) (int64, error) { return 180, nil },
		func(id string, _, _ int64) (bool, error) { return id != "stolen", nil },
		func(r jobstore.ScheduleRecord) (string, error) {
			submitted = append(submitted, r.ID)
			if r.ID == "fail" {
				return "", errors.New("boom")
			}
			return "job-" + r.ID, nil
		},
		func(id, jobID string) { set = append(set, id+":"+jobID) },
		func(string, int) {},
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, []string{"ok", "fail"}, submitted)
	assert.Eq(t, []string{"ok:job-ok"}, set)
}

func TestSweepSchedulesOnceSubmitsAndDisables(t *testing.T) {
	submits, setLast := 0, 0
	disabled := make([]string, 0)
	nextCalls := 0
	sweepSchedules(120, []jobstore.ScheduleRecord{{ID: "once-1", ScheduleType: "once", NextRunAt: 60, CatchUp: 0}}, 1,
		func(string, int64) (int64, error) {
			nextCalls++
			return 0, nil
		},
		func(id string, oldNext, newNext int64) (bool, error) {
			assert.Eq(t, "once-1", id)
			assert.Eq(t, int64(60), oldNext)
			assert.Eq(t, int64(0), newNext)
			return true, nil
		},
		func(r jobstore.ScheduleRecord) (string, error) {
			submits++
			assert.Eq(t, "once-1", r.ID)
			return "job-once", nil
		},
		func(id, jobID string) {
			setLast++
			assert.Eq(t, "once-1", id)
			assert.Eq(t, "job-once", jobID)
		},
		func(id string, enabled int) {
			disabled = append(disabled, id)
			assert.Eq(t, 0, enabled)
		},
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, nextCalls)
	assert.Eq(t, 1, submits)
	assert.Eq(t, 1, setLast)
	assert.Eq(t, []string{"once-1"}, disabled)
}

func TestSweepSchedulesOnceAdvanceFalseSkipsSubmitAndDisable(t *testing.T) {
	submits, disables := 0, 0
	sweepSchedules(120, []jobstore.ScheduleRecord{{ID: "once-1", ScheduleType: "once", NextRunAt: 60}}, 1,
		func(string, int64) (int64, error) { return 0, nil },
		func(string, int64, int64) (bool, error) { return false, nil },
		func(jobstore.ScheduleRecord) (string, error) {
			submits++
			return "job-once", nil
		},
		func(string, string) {},
		func(string, int) { disables++ },
		func(string, ...any) {}, func(string, ...any) {})
	assert.Eq(t, 0, submits)
	assert.Eq(t, 0, disables)
}
