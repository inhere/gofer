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

// 停用用户触发 schedule（配置入库 P1，计划 T5）：定时运行的真实入口是 submitDueSchedule，它走与
// 手动提交同一个准入，因此「调用方被停用」「调用方 id 与 worker 同 id 被封锁」都在这里被拒，并把
// 拒绝记到 schedule 自己的事件流上——定时器保留，丢的是这一次运行。

// storedCallerHash is a syntactically valid stored token hash (the job layer never reads it).
const storedCallerHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// scheduleServiceWith returns a service over cfg, opened on a fresh store, plus the upgrade permit
// submitDueSchedule requires.
func scheduleServiceWith(t *testing.T, cfg *config.Config) (*job.Service, *job.AdmissionPermit) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	jobs := job.NewService(cfg, nil, nil, nil, st, nil)
	permit, err := jobs.BeginUpgradeWork()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(permit.Release)
	return jobs, permit
}

// scheduleRunFailedDetail returns the detail of the schedule's run-failed event.
func scheduleRunFailedDetail(t *testing.T, jobs *job.Service, scheduleID string) string {
	t.Helper()
	evs, err := jobs.Meta().ListJobEvents(job.ScheduleEventScope(scheduleID), 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == job.EventScheduleRunFailed {
			return ev.Detail
		}
	}
	t.Fatalf("schedule %s has no %s event: %+v", scheduleID, job.EventScheduleRunFailed, evs)
	return ""
}

// TestSubmitDueScheduleRefusesDisabledCaller: a due run whose CREATOR is a disabled stored caller
// starts nothing, and the refusal is recorded on the schedule's own stream.
func TestSubmitDueScheduleRefusesDisabledCaller(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{{
		ID: "dbuser", Source: config.CallerSourceDB, TokenHash: storedCallerHash, Disabled: true,
	}}}}
	jobs, permit := scheduleServiceWith(t, cfg)

	r := jobstore.ScheduleRecord{
		ID: "sch-disabled", ProjectKey: "self", CreatorCaller: "dbuser",
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":"."}`,
	}
	if _, err := submitDueSchedule(jobs, permit, r); !errors.Is(err, job.ErrCeilingDenied) {
		t.Fatalf("submitDueSchedule err = %v, want ErrCeilingDenied", err)
	}
	if detail := scheduleRunFailedDetail(t, jobs, "sch-disabled"); !strings.Contains(detail, "disabled") {
		t.Fatalf("detail = %q, want the caller-disabled refusal", detail)
	}
	if list, err := jobs.Meta().ListJobs(jobstore.ListQuery{}); err != nil {
		t.Fatalf("ListJobs: %v", err)
	} else if len(list) != 0 {
		t.Fatalf("a refused schedule run created %d job(s)", len(list))
	}
}

// TestSubmitDueScheduleRefusesCallerHiddenByAWorker: a stored caller sharing a WORKER's id is hidden
// by the merge and its id is blocked (D10), so a schedule it still owns starts nothing — the work
// of that id may not run, whoever asks for it.
func TestSubmitDueScheduleRefusesCallerHiddenByAWorker(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
	}}
	// The merge hides the row (the worker credential wins the id) and blocks the owner.
	_, err := config.ApplyStoredCallers(cfg, []config.StoredCallerInput{{
		ID: "w1", TokenHash: storedCallerHash,
	}})
	if err != nil {
		t.Fatalf("ApplyStoredCallers: %v", err)
	}
	if !cfg.OwnerBlocked("w1") {
		t.Fatal("a stored caller sharing a worker's id must block that owner")
	}
	jobs, permit := scheduleServiceWith(t, cfg)

	r := jobstore.ScheduleRecord{
		ID: "sch-worker-id", ProjectKey: "self", CreatorCaller: "w1",
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"],"cwd":"."}`,
	}
	if _, err := submitDueSchedule(jobs, permit, r); !errors.Is(err, job.ErrCeilingDenied) {
		t.Fatalf("submitDueSchedule err = %v, want ErrCeilingDenied", err)
	}
	if detail := scheduleRunFailedDetail(t, jobs, "sch-worker-id"); !strings.Contains(detail, "disabled") {
		t.Fatalf("detail = %q, want the blocked-owner refusal", detail)
	}
}
