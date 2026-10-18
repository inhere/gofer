package serve

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestSubmitDueScheduleRecordsCeilingDenial: a due run whose CREATOR may no longer reach
// the project is refused by admission and the refusal is recorded on the schedule's own
// stream (design 2026-10-18 §准入: 定时运行同样终止并记录). The schedule itself is KEPT — the
// sweep has already advanced its next instant — so the run is what is lost, not the timer.
func TestSubmitDueScheduleRecordsCeilingDenial(t *testing.T) {
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "ci-bot", Ceiling: &config.CallerCeiling{Projects: []string{"other"}}},
	}}}
	jobs := job.NewService(cfg, nil, nil, nil, st, nil)
	permit, err := jobs.BeginUpgradeWork()
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Release()

	r := jobstore.ScheduleRecord{
		ID: "sch-ceil", ProjectKey: "self", CreatorCaller: "ci-bot",
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":"."}`,
	}
	if _, serr := submitDueSchedule(jobs, permit, r); !errors.Is(serr, job.ErrCeilingDenied) {
		t.Fatalf("submitDueSchedule err = %v, want ErrCeilingDenied", serr)
	}

	evs, err := st.ListJobEvents(job.ScheduleEventScope("sch-ceil"), 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != job.EventScheduleRunFailed {
		t.Fatalf("schedule events = %+v, want one %s", evs, job.EventScheduleRunFailed)
	}
	if !strings.Contains(evs[0].Detail, "ceiling") {
		t.Fatalf("detail = %q, want the ceiling refusal", evs[0].Detail)
	}
	if list, lerr := st.ListJobs(jobstore.ListQuery{}); lerr != nil {
		t.Fatalf("ListJobs: %v", lerr)
	} else if len(list) != 0 {
		t.Fatalf("a denied schedule run created %d job(s)", len(list))
	}
}
