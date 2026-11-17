package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestPlanDriverRoundTrip: a plan's driver state is one row — on with a level and a
// budget, changed in place by a second write, and gone when it is turned off.
func TestPlanDriverRoundTrip(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-driver-rt", Title: "driven", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))

	// No row = the driver is off (an upgrade turns nothing on).
	_, ok, err := s.GetPlanDriver("plan-driver-rt")
	assert.NoErr(t, err)
	assert.False(t, ok)

	assert.NoErr(t, s.UpsertPlanDriver(PlanDriver{
		PlanID: "plan-driver-rt", Level: "record",
		BudgetJSON: `{"max_jobs":10,"max_rework":2,"max_hours":1.5}`,
		EnabledAt:  100, UpdatedAt: 100,
	}))
	d, ok, err := s.GetPlanDriver("plan-driver-rt")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "record", d.Level)
	assert.Eq(t, `{"max_jobs":10,"max_rework":2,"max_hours":1.5}`, d.BudgetJSON)
	assert.Eq(t, "{}", d.SpentJSON, "a row written without spend reads back as an empty blob")
	assert.Eq(t, int64(100), d.EnabledAt)
	assert.Eq(t, int64(100), d.UpdatedAt)

	// A second write replaces the level, the budget and the accounting in place, and moves
	// the window (enabling or re-budgeting starts fresh).
	assert.NoErr(t, s.UpsertPlanDriver(PlanDriver{
		PlanID: "plan-driver-rt", Level: "record", BudgetJSON: `{"max_jobs":3}`,
		SpentJSON: `{"jobs":1}`, EnabledAt: 500, UpdatedAt: 500,
	}))
	d, ok, err = s.GetPlanDriver("plan-driver-rt")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, `{"max_jobs":3}`, d.BudgetJSON)
	assert.Eq(t, `{"jobs":1}`, d.SpentJSON)
	assert.Eq(t, int64(500), d.EnabledAt)
	assert.Eq(t, int64(500), d.UpdatedAt)

	// A half-built row is refused instead of written.
	assert.Err(t, s.UpsertPlanDriver(PlanDriver{PlanID: "plan-driver-rt"}), "level is required")
	assert.Err(t, s.UpsertPlanDriver(PlanDriver{Level: "record"}), "plan id is required")

	// The list holds every enabled driver, oldest first.
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-driver-late", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.UpsertPlanDriver(PlanDriver{PlanID: "plan-driver-late", Level: "record", EnabledAt: 200, UpdatedAt: 200}))
	list, err := s.ListPlanDrivers()
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(list))
	assert.Eq(t, "plan-driver-late", list[0].PlanID)
	assert.Eq(t, "plan-driver-rt", list[1].PlanID)
	assert.Eq(t, "{}", list[0].BudgetJSON)

	// Turning it off deletes the row, and doing it twice is not an error.
	removed, err := s.DeletePlanDriver("plan-driver-rt")
	assert.NoErr(t, err)
	assert.True(t, removed)
	removed, err = s.DeletePlanDriver("plan-driver-rt")
	assert.NoErr(t, err)
	assert.False(t, removed)
	_, ok, err = s.GetPlanDriver("plan-driver-rt")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

// TestArchivePlanEndsItsDriver: archiving a plan deletes the driver row (this store has
// no plan delete of its own), while every other status change leaves it alone.
func TestArchivePlanEndsItsDriver(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-driver-arch", Status: PlanOpen, CreatedAt: 1, UpdatedAt: 1}))
	assert.NoErr(t, s.UpsertPlanDriver(PlanDriver{PlanID: "plan-driver-arch", Level: "record"}))

	// A done plan keeps its driver: the row is only about the plan's life, not its status.
	assert.NoErr(t, s.SetPlanStatus("plan-driver-arch", PlanDone, -1))
	if _, ok, err := s.GetPlanDriver("plan-driver-arch"); err != nil || !ok {
		t.Fatalf("a done plan kept no driver: ok=%v err=%v", ok, err)
	}

	assert.NoErr(t, s.SetPlanStatus("plan-driver-arch", PlanArchived, -1))
	if d, ok, err := s.GetPlanDriver("plan-driver-arch"); err != nil || ok {
		t.Fatalf("an archived plan still has a driver: %+v ok=%v err=%v", d, ok, err)
	}
}
