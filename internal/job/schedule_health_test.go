package job

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// healthSchedule stores a schedule whose request runs the given exec command.
func healthSchedule(t *testing.T, s *Service, id string, cmd []string, mutate func(*jobstore.ScheduleRecord)) jobstore.ScheduleRecord {
	t.Helper()
	raw, err := json.Marshal(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: cmd, Cwd: ".", TimeoutSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	rec := jobstore.ScheduleRecord{
		ID: id, Name: id, ScheduleType: "cron", CronExpr: "* * * * *", RequestJSON: string(raw),
		Enabled: 1, NextRunAt: 100, ProjectKey: "self", Overlap: jobstore.ScheduleOverlapSkip, MaxFailures: 3,
		CreatedAt: 1, UpdatedAt: 1,
	}
	if mutate != nil {
		mutate(&rec)
	}
	if err := s.meta.InsertSchedule(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func scheduleEventTypes(t *testing.T, s *Service, id string) []string {
	t.Helper()
	evs, err := s.meta.ListJobEvents(ScheduleEventScope(id), 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}

func countType(types []string, want string) int {
	n := 0
	for _, ty := range types {
		if ty == want {
			n++
		}
	}
	return n
}

func getSchedule(t *testing.T, s *Service, id string) jobstore.ScheduleRecord {
	t.Helper()
	rec, ok, err := s.meta.GetSchedule(id)
	if err != nil || !ok {
		t.Fatalf("GetSchedule(%s) ok=%v err=%v", id, ok, err)
	}
	return rec
}

// fireTimerAndWait fires one timer run and waits for its job to finish.
func fireTimerAndWait(t *testing.T, s *Service, rec jobstore.ScheduleRecord) JobResult {
	t.Helper()
	id, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, rec.ID))
	if err != nil || id == "" {
		t.Fatalf("FireTimer id=%q err=%v", id, err)
	}
	final, ok := s.Wait(id)
	if !ok {
		t.Fatalf("job %s not found", id)
	}
	return final
}

// A timer run stamps the schedule and the trigger on the job, adds to run_count, and
// leaves the schedule enabled below its limits.
func TestScheduleTimerRunStampsJobAndCountsRun(t *testing.T) {
	s := newTestService(t, t.TempDir())
	rec := healthSchedule(t, s, "sch-stamp", []string{"go", "version"}, nil)
	final := fireTimerAndWait(t, s, rec)
	if final.ScheduleID != "sch-stamp" || final.ScheduleTrigger != jobstore.ScheduleTriggerTimer {
		t.Fatalf("job schedule = %q/%q", final.ScheduleID, final.ScheduleTrigger)
	}
	row, _, _ := s.meta.GetJob(final.ID)
	if row.ScheduleID != "sch-stamp" || row.ScheduleTrigger != jobstore.ScheduleTriggerTimer {
		t.Fatalf("row schedule = %q/%q", row.ScheduleID, row.ScheduleTrigger)
	}
	got := getSchedule(t, s, "sch-stamp")
	if got.RunCount != 1 || got.Enabled != 1 {
		t.Fatalf("run_count=%d enabled=%d, want 1/1", got.RunCount, got.Enabled)
	}
}

// overlap=skip: while the previous run's job is unfinished the next tick submits
// nothing and writes schedule.run_skipped naming that job; once it ends the next tick
// runs. overlap=allow submits regardless.
func TestScheduleOverlapSkipAndAllow(t *testing.T) {
	s := newTestService(t, t.TempDir())
	rec := healthSchedule(t, s, "sch-ov", []string{"sleep", "60"}, nil)
	firstID, err := s.ScheduleDispatcher().FireTimer(nil, rec)
	if err != nil || firstID == "" {
		t.Fatalf("first FireTimer id=%q err=%v", firstID, err)
	}
	wait.Until(t, 10*time.Second, "first job running", func() bool {
		r, _ := s.Get(firstID)
		return r.Status == StatusRunning
	})

	id2, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, "sch-ov"))
	if err != nil || id2 != "" {
		t.Fatalf("overlapping FireTimer id=%q err=%v, want a skip", id2, err)
	}
	types := scheduleEventTypes(t, s, "sch-ov")
	if countType(types, EventScheduleRunSkipped) != 1 {
		t.Fatalf("events = %v, want one %s", types, EventScheduleRunSkipped)
	}
	evs, _ := s.meta.ListJobEvents(ScheduleEventScope("sch-ov"), 0)
	var d struct {
		Reason string `json:"reason"`
		JobID  string `json:"job_id"`
	}
	_ = json.Unmarshal([]byte(evs[0].Detail), &d)
	if d.Reason != ScheduleSkipPreviousRunning || d.JobID != firstID {
		t.Fatalf("skip detail = %+v, want previous_running / %s", d, firstID)
	}
	if got := getSchedule(t, s, "sch-ov"); got.RunCount != 1 {
		t.Fatalf("run_count = %d after a skip, want 1", got.RunCount)
	}

	// overlap=allow ignores the unfinished job.
	setOverlap(t, s, "sch-ov", jobstore.ScheduleOverlapAllow)
	id3, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, "sch-ov"))
	if err != nil || id3 == "" {
		t.Fatalf("allow FireTimer id=%q err=%v", id3, err)
	}

	for _, id := range []string{firstID, id3} {
		_ = s.Cancel(id)
		s.Wait(id)
	}
	// Back to skip: with nothing unfinished the next tick runs.
	setOverlap(t, s, "sch-ov", jobstore.ScheduleOverlapSkip)
	setCmd(t, s, "sch-ov", []string{"go", "version"})
	id4, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, "sch-ov"))
	if err != nil || id4 == "" {
		t.Fatalf("FireTimer after the previous ended id=%q err=%v", id4, err)
	}
	s.Wait(id4)
}

// setCmd replaces the command of the schedule's stored request.
func setCmd(t *testing.T, s *Service, id string, cmd []string) {
	t.Helper()
	cur := getSchedule(t, s, id)
	err := s.meta.UpdateSchedule(id, jobstore.ScheduleUpdate{
		Name: cur.Name, ScheduleType: cur.ScheduleType, CronExpr: cur.CronExpr, RequestJSON: healthRequestJSON(t, cmd),
		Enabled: cur.Enabled, NextRunAt: cur.NextRunAt, Overlap: cur.Overlap, MaxRuns: cur.MaxRuns, MaxFailures: cur.MaxFailures,
		PrecheckJSON: cur.PrecheckJSON, PrecheckFingerprint: cur.PrecheckFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func setOverlap(t *testing.T, s *Service, id, overlap string) {
	t.Helper()
	cur := getSchedule(t, s, id)
	err := s.meta.UpdateSchedule(id, jobstore.ScheduleUpdate{
		Name: cur.Name, ScheduleType: cur.ScheduleType, CronExpr: cur.CronExpr, RequestJSON: cur.RequestJSON,
		Enabled: cur.Enabled, NextRunAt: cur.NextRunAt, Overlap: overlap, MaxRuns: cur.MaxRuns, MaxFailures: cur.MaxFailures,
		PrecheckJSON: cur.PrecheckJSON, PrecheckFingerprint: cur.PrecheckFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func healthRequestJSON(t *testing.T, cmd []string) string {
	t.Helper()
	raw, err := json.Marshal(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: cmd, Cwd: ".", TimeoutSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A run-now / webhook run is stamped with its schedule (so overlap sees it) but is
// never counted: neither a run nor a failure, and a success does not clear a streak.
func TestScheduleNonTimerTriggersAreNotCounted(t *testing.T) {
	s := newTestService(t, t.TempDir())
	rec := healthSchedule(t, s, "sch-nt", []string{"sh", "-c", "exit 3"}, nil)
	_, _, _, _ = s.meta.IncScheduleFailure("sch-nt") // an existing streak of 1
	for _, trig := range []string{jobstore.ScheduleTriggerRunNow, jobstore.ScheduleTriggerWebhook} {
		var req JobRequest
		_ = json.Unmarshal([]byte(rec.RequestJSON), &req)
		run, err := s.ScheduleDispatcher().Dispatch(nil, getSchedule(t, s, "sch-nt"), trig, req)
		if err != nil || run.Skipped {
			t.Fatalf("%s: run=%+v err=%v", trig, run, err)
		}
		final, _ := s.Wait(run.Result.ID)
		if final.ScheduleID != "sch-nt" || final.ScheduleTrigger != trig || final.Status != StatusFailed {
			t.Fatalf("%s job = %s/%s/%s", trig, final.ScheduleID, final.ScheduleTrigger, final.Status)
		}
	}
	got := getSchedule(t, s, "sch-nt")
	if got.RunCount != 0 || got.ConsecutiveFailures != 1 || got.Enabled != 1 {
		t.Fatalf("run_count=%d failures=%d enabled=%d, want 0/1/1", got.RunCount, got.ConsecutiveFailures, got.Enabled)
	}
}

// Failed / timeout add to the streak, done clears it, cancelled leaves it alone; the
// streak reaching max_failures pauses the schedule and announces it exactly once.
func TestScheduleFailureStreakPausesAndAnnouncesOnce(t *testing.T) {
	s := newTestService(t, t.TempDir())
	rec := healthSchedule(t, s, "sch-ff", []string{"sh", "-c", "exit 3"}, func(r *jobstore.ScheduleRecord) { r.MaxFailures = 3 })

	fireTimerAndWait(t, s, rec)
	fireTimerAndWait(t, s, rec)
	if got := getSchedule(t, s, "sch-ff"); got.ConsecutiveFailures != 2 || got.Enabled != 1 {
		t.Fatalf("after 2 failures: failures=%d enabled=%d, want 2/1", got.ConsecutiveFailures, got.Enabled)
	}

	// A success clears the streak.
	setCmd(t, s, "sch-ff", []string{"go", "version"})
	fireTimerAndWait(t, s, rec)
	if got := getSchedule(t, s, "sch-ff"); got.ConsecutiveFailures != 0 {
		t.Fatalf("failures = %d after a done run, want 0", got.ConsecutiveFailures)
	}

	// A cancelled run changes nothing.
	setCmd(t, s, "sch-ff", []string{"sleep", "60"})
	_, _, _, _ = s.meta.IncScheduleFailure("sch-ff")
	id, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, "sch-ff"))
	if err != nil || id == "" {
		t.Fatalf("FireTimer id=%q err=%v", id, err)
	}
	wait.Until(t, 10*time.Second, "slow job running", func() bool { r, _ := s.Get(id); return r.Status == StatusRunning })
	_ = s.Cancel(id)
	s.Wait(id)
	if got := getSchedule(t, s, "sch-ff"); got.ConsecutiveFailures != 1 {
		t.Fatalf("failures = %d after a cancelled run, want the earlier 1", got.ConsecutiveFailures)
	}
	_ = s.meta.ResetScheduleFailures("sch-ff")

	// Three failures in a row: the third pauses.
	setCmd(t, s, "sch-ff", []string{"sh", "-c", "exit 3"})
	for i := 0; i < 3; i++ {
		fireTimerAndWait(t, s, getSchedule(t, s, "sch-ff"))
	}
	got := getSchedule(t, s, "sch-ff")
	if got.Enabled != 0 || got.PausedReason != jobstore.SchedulePausedFailures || got.ConsecutiveFailures != 3 {
		t.Fatalf("paused? enabled=%d reason=%q failures=%d", got.Enabled, got.PausedReason, got.ConsecutiveFailures)
	}
	types := scheduleEventTypes(t, s, "sch-ff")
	if n := countType(types, EventSchedulePaused); n != 1 {
		t.Fatalf("events = %v, want exactly one %s", types, EventSchedulePaused)
	}
}

// A timeout counts as a failure too.
func TestScheduleTimeoutCountsAsFailure(t *testing.T) {
	s := newTestService(t, t.TempDir())
	raw, _ := json.Marshal(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 1})
	healthSchedule(t, s, "sch-to", []string{"true"}, func(r *jobstore.ScheduleRecord) { r.RequestJSON = string(raw) })
	final := fireTimerAndWait(t, s, jobstore.ScheduleRecord{ID: "sch-to"})
	if final.Status != StatusTimeout {
		t.Fatalf("status = %s, want timeout", final.Status)
	}
	if got := getSchedule(t, s, "sch-to"); got.ConsecutiveFailures != 1 {
		t.Fatalf("failures = %d after a timeout, want 1", got.ConsecutiveFailures)
	}
}

// A job that parks in needs_review is finished work: it clears the streak.
func TestScheduleNeedsReviewClearsStreak(t *testing.T) {
	s := newTestService(t, t.TempDir())
	raw, _ := json.Marshal(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60, Review: true})
	healthSchedule(t, s, "sch-nr", []string{"true"}, func(r *jobstore.ScheduleRecord) { r.RequestJSON = string(raw) })
	_, _, _, _ = s.meta.IncScheduleFailure("sch-nr")
	final := fireTimerAndWait(t, s, jobstore.ScheduleRecord{ID: "sch-nr"})
	if final.Status != StatusNeedsReview {
		t.Fatalf("status = %s, want needs_review", final.Status)
	}
	if got := getSchedule(t, s, "sch-nr"); got.ConsecutiveFailures != 0 {
		t.Fatalf("failures = %d, want 0", got.ConsecutiveFailures)
	}
}

// A run that cannot be submitted (here: the request is refused) is a failed run: it
// writes schedule.run_failed, adds to the streak and can pause the schedule.
func TestScheduleSubmitFailureCountsAndPauses(t *testing.T) {
	s := newTestService(t, t.TempDir())
	raw, _ := json.Marshal(JobRequest{ProjectKey: "noexec", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: "."})
	rec := healthSchedule(t, s, "sch-sf", []string{"true"}, func(r *jobstore.ScheduleRecord) {
		r.RequestJSON = string(raw)
		r.MaxFailures = 2
	})
	for i := 0; i < 2; i++ {
		if id, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, rec.ID)); err == nil || id != "" {
			t.Fatalf("FireTimer id=%q err=%v, want a refusal", id, err)
		}
	}
	got := getSchedule(t, s, "sch-sf")
	if got.ConsecutiveFailures != 2 || got.Enabled != 0 || got.PausedReason != jobstore.SchedulePausedFailures || got.RunCount != 0 {
		t.Fatalf("failures=%d enabled=%d reason=%q runs=%d", got.ConsecutiveFailures, got.Enabled, got.PausedReason, got.RunCount)
	}
	types := scheduleEventTypes(t, s, "sch-sf")
	if countType(types, EventScheduleRunFailed) != 2 || countType(types, EventSchedulePaused) != 1 {
		t.Fatalf("events = %v, want 2 run_failed + 1 paused", types)
	}
}

// A refused run-now does not touch the streak (only the ceiling refusal is recorded).
func TestScheduleRunNowRefusalNotCounted(t *testing.T) {
	s := newTestService(t, t.TempDir())
	rec := healthSchedule(t, s, "sch-rn", []string{"true"}, nil)
	req := JobRequest{ProjectKey: "noexec", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: "."}
	if _, err := s.ScheduleDispatcher().Dispatch(nil, rec, jobstore.ScheduleTriggerRunNow, req); err == nil {
		t.Fatal("want a refusal")
	}
	if got := getSchedule(t, s, "sch-rn"); got.ConsecutiveFailures != 0 {
		t.Fatalf("failures = %d, want 0", got.ConsecutiveFailures)
	}
	if types := scheduleEventTypes(t, s, "sch-rn"); len(types) != 0 {
		t.Fatalf("events = %v, want none for a non-ceiling run-now refusal", types)
	}
}

// max_runs: the Nth timer run stops the schedule (reason max_runs, one event); a later
// tick of a schedule enabled again without a higher limit submits nothing.
func TestScheduleMaxRunsStopsSchedule(t *testing.T) {
	s := newTestService(t, t.TempDir())
	rec := healthSchedule(t, s, "sch-mr", []string{"go", "version"}, func(r *jobstore.ScheduleRecord) { r.MaxRuns = 2 })
	fireTimerAndWait(t, s, rec)
	if got := getSchedule(t, s, "sch-mr"); got.Enabled != 1 || got.RunCount != 1 {
		t.Fatalf("after run 1: enabled=%d runs=%d", got.Enabled, got.RunCount)
	}
	fireTimerAndWait(t, s, rec)
	got := getSchedule(t, s, "sch-mr")
	if got.Enabled != 0 || got.PausedReason != jobstore.SchedulePausedMaxRuns || got.RunCount != 2 {
		t.Fatalf("after run 2: enabled=%d reason=%q runs=%d", got.Enabled, got.PausedReason, got.RunCount)
	}
	if n := countType(scheduleEventTypes(t, s, "sch-mr"), EventSchedulePaused); n != 1 {
		t.Fatalf("paused events = %d, want 1", n)
	}

	// Enabled again, limit unchanged: no further run.
	if err := s.meta.SetScheduleEnabled("sch-mr", 1); err != nil {
		t.Fatal(err)
	}
	id, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, "sch-mr"))
	if err != nil || id != "" {
		t.Fatalf("FireTimer past max_runs id=%q err=%v, want none", id, err)
	}
	if got := getSchedule(t, s, "sch-mr"); got.RunCount != 2 || got.Enabled != 0 {
		t.Fatalf("runs=%d enabled=%d, want 2/0", got.RunCount, got.Enabled)
	}
}

// A `once` schedule uses no run-health fields: no overlap skip, no counters, no pause.
func TestScheduleOnceIgnoresRunHealth(t *testing.T) {
	s := newTestService(t, t.TempDir())
	rec := healthSchedule(t, s, "sch-once", []string{"sh", "-c", "exit 3"}, func(r *jobstore.ScheduleRecord) {
		r.ScheduleType = "once"
		r.CronExpr = ""
		r.MaxFailures = 1
		r.MaxRuns = 1
	})
	final := fireTimerAndWait(t, s, rec)
	if final.Status != StatusFailed {
		t.Fatalf("status = %s", final.Status)
	}
	got := getSchedule(t, s, "sch-once")
	if got.RunCount != 0 || got.ConsecutiveFailures != 0 || got.PausedReason != "" {
		t.Fatalf("once schedule counters = runs %d failures %d reason %q", got.RunCount, got.ConsecutiveFailures, got.PausedReason)
	}
}

// A ceiling refusal of a timer run is still recorded once as schedule.run_failed (the
// behaviour the sweep had before the dispatcher), now also counted.
func TestScheduleCeilingDenialRecordedAndCounted(t *testing.T) {
	s := newTestService(t, t.TempDir())
	cfg := *s.config()
	cfg.Server.Callers = []config.CallerConfig{{ID: "ci-bot", Ceiling: &config.CallerCeiling{Projects: []string{"other"}}}}
	s.Reload(&cfg)
	rec := healthSchedule(t, s, "sch-ce", []string{"go", "version"}, func(r *jobstore.ScheduleRecord) { r.CreatorCaller = "ci-bot" })
	if _, err := s.ScheduleDispatcher().FireTimer(nil, rec); !errors.Is(err, ErrCeilingDenied) {
		t.Fatalf("err = %v, want ErrCeilingDenied", err)
	}
	if n := countType(scheduleEventTypes(t, s, "sch-ce"), EventScheduleRunFailed); n != 1 {
		t.Fatalf("run_failed events = %d, want 1", n)
	}
	if got := getSchedule(t, s, "sch-ce"); got.ConsecutiveFailures != 1 {
		t.Fatalf("failures = %d, want 1", got.ConsecutiveFailures)
	}
}
