package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// Plan driver state (plan R4): the switches, their refusals and what `plan drive status`
// reads back. The rules that decide an action live in the driver package and are not
// exercised here.

func seedPlanForDriver(t *testing.T, svc *Service, planID string) {
	t.Helper()
	assert.NoErr(t, svc.meta.InsertPlan(jobstore.Plan{
		PlanID: planID, Title: "driven", Status: jobstore.PlanOpen, ProjectKey: "self",
		CreatedAt: 1, UpdatedAt: 1,
	}))
}

// TestPlanDriverEnableStatusDisable: `plan drive on` stores the level and the budget,
// `status` reads them back, and `off` returns the plan to "no driver".
func TestPlanDriverEnableStatusDisable(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, t.TempDir())
	seedPlanForDriver(t, svc, "plan-drive-on")

	// Before enabling: off, with nothing to show.
	st, err := svc.PlanDriverStatus("plan-drive-on")
	assert.NoErr(t, err)
	assert.False(t, st.Enabled)
	assert.Eq(t, "", st.Level)
	assert.Eq(t, 0, st.Budget.MaxJobs)

	assert.NoErr(t, svc.EnablePlanDriver("plan-drive-on", PlanDriverLevelRecord,
		PlanDriverBudget{MaxJobs: 12, MaxRework: 3, MaxHours: 6.5}))
	st, err = svc.PlanDriverStatus("plan-drive-on")
	assert.NoErr(t, err)
	assert.True(t, st.Enabled)
	assert.Eq(t, PlanDriverLevelRecord, st.Level)
	assert.Eq(t, 12, st.Budget.MaxJobs)
	assert.Eq(t, 3, st.Budget.MaxRework)
	assert.Eq(t, 6.5, st.Budget.MaxHours)
	assert.True(t, st.EnabledAt > 0, "enabling stamps the accounting window")

	// An empty level means the default: record.
	assert.NoErr(t, svc.EnablePlanDriver("plan-drive-on", "", PlanDriverBudget{}))
	st, err = svc.PlanDriverStatus("plan-drive-on")
	assert.NoErr(t, err)
	assert.Eq(t, PlanDriverLevelRecord, st.Level)
	assert.Eq(t, 0, st.Budget.MaxJobs, "0 = unlimited")

	// Off is idempotent: asking twice is not an error, and the row is gone.
	assert.NoErr(t, svc.DisablePlanDriver("plan-drive-on"))
	assert.NoErr(t, svc.DisablePlanDriver("plan-drive-on"))
	st, err = svc.PlanDriverStatus("plan-drive-on")
	assert.NoErr(t, err)
	assert.False(t, st.Enabled)
}

// TestPlanDriverRejectsLaterLevels: `assist` and `auto` do not exist in this batch, so
// enabling them is refused with a message that says so (never a silent downgrade to
// record), and nothing is stored.
func TestPlanDriverRejectsLaterLevels(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, t.TempDir())
	seedPlanForDriver(t, svc, "plan-drive-later")
	lang := svc.notifyLang("self")

	for _, level := range []string{PlanDriverLevelAssist, PlanDriverLevelAuto, " ASSIST "} {
		clean := strings.ToLower(strings.TrimSpace(level))
		want := i18n.T(lang, "job.plan_driver.level_later", "level", clean)
		if strings.HasPrefix(want, "job.plan_driver.") {
			t.Fatalf("the later-version text is missing from the %s catalog", lang)
		}
		err := svc.EnablePlanDriver("plan-drive-later", level, PlanDriverBudget{})
		if !errors.Is(err, PlanDriverInvalid) {
			t.Fatalf("level %q: err = %v, want PlanDriverInvalid", level, err)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("level %q: err = %q, want %q", level, err, want)
		}
	}
	st, err := svc.PlanDriverStatus("plan-drive-later")
	assert.NoErr(t, err)
	assert.False(t, st.Enabled, "a refused level writes nothing")

	// A level nobody defined is a plain parameter error too.
	err = svc.EnablePlanDriver("plan-drive-later", "turbo", PlanDriverBudget{})
	if !errors.Is(err, PlanDriverInvalid) || !strings.Contains(err.Error(), "turbo") {
		t.Fatalf("unknown level: err = %v, want PlanDriverInvalid naming the level", err)
	}
}

// TestPlanDriverCatalogIsBilingual: the refusals above are catalog text, present in both
// languages and not the bare key (i18n.T falls back to the key for a missing entry).
func TestPlanDriverCatalogIsBilingual(t *testing.T) {
	t.Parallel()
	for _, key := range []string{
		"job.plan_driver.level_later", "job.plan_driver.level_unknown",
		"job.plan_driver.budget_negative", "job.plan_driver.budget_not_finite",
	} {
		en := i18n.T(i18n.EN, key, "level", "assist", "field", "max_jobs")
		zh := i18n.T(i18n.ZhCN, key, "level", "assist", "field", "max_jobs")
		if en == key || zh == key {
			t.Fatalf("%s is missing from a catalog (en=%q zh=%q)", key, en, zh)
		}
		if en == zh {
			t.Fatalf("%s reads the same in both languages: %q", key, en)
		}
	}
}

// TestPlanDriverRejectsNegativeBudget: a negative ceiling is a typo, not a smaller limit
// (0 is the unlimited value), so it is refused before anything is written.
func TestPlanDriverRejectsNegativeBudget(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, t.TempDir())
	seedPlanForDriver(t, svc, "plan-drive-budget")

	for name, budget := range map[string]PlanDriverBudget{
		"max_jobs":   {MaxJobs: -1},
		"max_rework": {MaxRework: -2},
		"max_hours":  {MaxHours: -0.5},
	} {
		err := svc.EnablePlanDriver("plan-drive-budget", PlanDriverLevelRecord, budget)
		if !errors.Is(err, PlanDriverInvalid) {
			t.Fatalf("%s: err = %v, want PlanDriverInvalid", name, err)
		}
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err = %q, want the field named", name, err)
		}
	}
	st, err := svc.PlanDriverStatus("plan-drive-budget")
	assert.NoErr(t, err)
	assert.False(t, st.Enabled, "a refused budget writes nothing")
}

// TestPlanDriverUnknownPlan: both switches and the read report an unknown plan with the
// sentinel the HTTP layer maps to 404.
func TestPlanDriverUnknownPlan(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, t.TempDir())

	if err := svc.EnablePlanDriver("plan-no-such", PlanDriverLevelRecord, PlanDriverBudget{}); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("enable: err = %v, want ErrPlanNotFound", err)
	}
	if err := svc.DisablePlanDriver("plan-no-such"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("disable: err = %v, want ErrPlanNotFound", err)
	}
	if _, err := svc.PlanDriverStatus("plan-no-such"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("status: err = %v, want ErrPlanNotFound", err)
	}
}

// TestPlanDriverDisabledByArchive: archiving the plan ends the driver, so a re-opened
// archived plan is never silently driven.
func TestPlanDriverDisabledByArchive(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, t.TempDir())
	seedPlanForDriver(t, svc, "plan-drive-arch")
	assert.NoErr(t, svc.EnablePlanDriver("plan-drive-arch", PlanDriverLevelRecord, PlanDriverBudget{MaxJobs: 2}))

	assert.NoErr(t, svc.meta.SetPlanStatus("plan-drive-arch", jobstore.PlanArchived, -1))
	st, err := svc.PlanDriverStatus("plan-drive-arch")
	assert.NoErr(t, err)
	assert.False(t, st.Enabled, "an archived plan keeps no driver")
}
