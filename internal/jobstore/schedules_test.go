package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func sampleSchedule(id, project string, nextRunAt int64) ScheduleRecord {
	return ScheduleRecord{
		ID:          id,
		Name:        "nightly " + id,
		CronExpr:    "0 2 * * *",
		RequestJSON: `{"project_key":"` + project + `"}`,
		Enabled:     1,
		NextRunAt:   nextRunAt,
		CatchUp:     1,
		ProjectKey:  project,
		CreatedAt:   nextRunAt - 100,
		UpdatedAt:   nextRunAt - 100,
	}
}

func TestSchedulesTableExists(t *testing.T) {
	s := openTest(t)
	for _, col := range []string{
		"id", "name", "cron_expr", "request_json", "enabled", "next_run_at",
		"last_run_at", "last_job_id", "catch_up", "project_key", "created_at", "updated_at",
	} {
		assert.True(t, tableHasColumn(t, s, "schedules", col))
	}
	assert.True(t, indexExists(t, s, "idx_sched_due"))
}

func TestScheduleCRUDRoundTrip(t *testing.T) {
	s := openTest(t)

	a := sampleSchedule("sch-a", "proj-a", 2000)
	b := sampleSchedule("sch-b", "proj-b", 3000)
	b.Enabled = 0
	assert.NoErr(t, s.InsertSchedule(a))
	assert.NoErr(t, s.InsertSchedule(b))

	got, ok, err := s.GetSchedule("sch-a")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "sch-a", got.ID)
	assert.Eq(t, "nightly sch-a", got.Name)
	assert.Eq(t, "0 2 * * *", got.CronExpr)
	assert.Eq(t, `{"project_key":"proj-a"}`, got.RequestJSON)
	assert.Eq(t, 1, got.Enabled)
	assert.Eq(t, int64(2000), got.NextRunAt)
	assert.Eq(t, int64(0), got.LastRunAt)
	assert.Eq(t, "", got.LastJobID)
	assert.Eq(t, 1, got.CatchUp)
	assert.Eq(t, "proj-a", got.ProjectKey)

	all, err := s.ListSchedules("", false)
	assert.NoErr(t, err)
	assert.Len(t, all, 2)
	projA, err := s.ListSchedules("proj-a", false)
	assert.NoErr(t, err)
	assert.Len(t, projA, 1)
	assert.Eq(t, "sch-a", projA[0].ID)
	enabled, err := s.ListSchedules("", true)
	assert.NoErr(t, err)
	assert.Len(t, enabled, 1)
	assert.Eq(t, "sch-a", enabled[0].ID)

	assert.NoErr(t, s.SetScheduleEnabled("sch-b", 1))
	got, ok, err = s.GetSchedule("sch-b")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, 1, got.Enabled)
	assert.NoErr(t, s.SetScheduleLastJob("sch-b", "job-1"))
	got, ok, err = s.GetSchedule("sch-b")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "job-1", got.LastJobID)

	assert.NoErr(t, s.DeleteSchedule("sch-a"))
	_, ok, err = s.GetSchedule("sch-a")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

func TestDueSchedules(t *testing.T) {
	s := openTest(t)
	now := int64(1000)
	dueLate := sampleSchedule("due-late", "proj", 900)
	dueEarly := sampleSchedule("due-early", "proj", 800)
	future := sampleSchedule("future", "proj", 1001)
	disabled := sampleSchedule("disabled", "proj", 700)
	disabled.Enabled = 0
	zero := sampleSchedule("zero", "proj", 0)
	for _, r := range []ScheduleRecord{dueLate, dueEarly, future, disabled, zero} {
		assert.NoErr(t, s.InsertSchedule(r))
	}

	got, err := s.DueSchedules(now)
	assert.NoErr(t, err)
	assert.Len(t, got, 2)
	assert.Eq(t, "due-early", got[0].ID)
	assert.Eq(t, "due-late", got[1].ID)
}

func TestAdvanceSchedule(t *testing.T) {
	s := openTest(t)
	rec := sampleSchedule("sch-adv", "proj", 1000)
	assert.NoErr(t, s.InsertSchedule(rec))

	ok, err := s.AdvanceSchedule("sch-adv", 1000, 2000, 1500)
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, found, err := s.GetSchedule("sch-adv")
	assert.NoErr(t, err)
	assert.True(t, found)
	assert.Eq(t, int64(2000), got.NextRunAt)
	assert.Eq(t, int64(1500), got.LastRunAt)
	assert.Eq(t, int64(1500), got.UpdatedAt)

	ok, err = s.AdvanceSchedule("sch-adv", 1000, 3000, 2500)
	assert.NoErr(t, err)
	assert.False(t, ok)
	got, found, err = s.GetSchedule("sch-adv")
	assert.NoErr(t, err)
	assert.True(t, found)
	assert.Eq(t, int64(2000), got.NextRunAt)
	assert.Eq(t, int64(1500), got.LastRunAt)
}

func TestNextCronRun(t *testing.T) {
	after := time.Date(2026, 6, 30, 12, 34, 12, 0, time.UTC)
	next, err := NextCronRun("*/1 * * * *", after)
	assert.NoErr(t, err)
	assert.Eq(t, time.Date(2026, 6, 30, 12, 35, 0, 0, time.UTC).Unix(), next)

	after = time.Date(2026, 6, 30, 2, 0, 0, 0, time.UTC)
	next, err = NextCronRun("0 2 * * *", after)
	assert.NoErr(t, err)
	assert.Eq(t, time.Date(2026, 7, 1, 2, 0, 0, 0, time.UTC).Unix(), next)

	_, err = NextCronRun("bad cron", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))
	assert.Err(t, err)
}
