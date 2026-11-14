package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// precheckSchedule stores a schedule with a pre-run check and the given job command.
func precheckSchedule(t *testing.T, s *Service, id string, jobCmd []string, pc SchedulePrecheck, mutate func(*jobstore.ScheduleRecord)) {
	t.Helper()
	raw, err := EncodeSchedulePrecheck(&pc)
	if err != nil {
		t.Fatal(err)
	}
	healthSchedule(t, s, id, jobCmd, func(r *jobstore.ScheduleRecord) {
		r.PrecheckJSON = raw
		if mutate != nil {
			mutate(r)
		}
	})
}

// fireChecked fires one tick (the check runs in the background) and returns immediately.
func fireChecked(t *testing.T, s *Service, id string) {
	t.Helper()
	jobID, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, id))
	if err != nil || jobID != "" {
		t.Fatalf("FireTimer of a checked schedule: id=%q err=%v, want an immediate no-id nil", jobID, err)
	}
}

func waitScheduleEvent(t *testing.T, s *Service, id, eventType string, n int) {
	t.Helper()
	wait.Until(t, 15*time.Second, "schedule event "+eventType, func() bool {
		return countType(scheduleEventTypes(t, s, id), eventType) >= n
	})
}

func scheduleJobs(t *testing.T, s *Service, scheduleID string) []jobstore.JobRecord {
	t.Helper()
	all, err := s.meta.ListJobs(jobstore.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var out []jobstore.JobRecord
	for _, r := range all {
		if r.ScheduleID == scheduleID {
			out = append(out, r)
		}
	}
	return out
}

func skippedReasons(t *testing.T, s *Service, id string) []string {
	t.Helper()
	evs, _ := s.meta.ListJobEvents(ScheduleEventScope(id), 0)
	var out []string
	for _, ev := range evs {
		if ev.Type != EventScheduleRunSkipped {
			continue
		}
		var d struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(ev.Detail), &d)
		out = append(out, d.Reason)
	}
	return out
}

// exit 0 submits the run, hands the (masked) output to the job as GOFER_PRECHECK_OUTPUT and
// stores the output fingerprint once the run was submitted.
func TestPrecheckExitZeroSubmitsWithOutput(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	precheckSchedule(t, s, "sch-p0", []string{"sh", "-c", `printf %s "$GOFER_PRECHECK_OUTPUT"`},
		SchedulePrecheck{Argv: []string{"sh", "-c", "echo 3 new issues"}}, nil)
	fireChecked(t, s, "sch-p0")
	wait.Until(t, 15*time.Second, "the checked run's job", func() bool { return len(scheduleJobs(t, s, "sch-p0")) == 1 })
	jobID := scheduleJobs(t, s, "sch-p0")[0].ID
	final, _ := s.Wait(jobID)
	if final.Status != StatusDone || final.ScheduleTrigger != jobstore.ScheduleTriggerTimer {
		t.Fatalf("job = %s / %s", final.Status, final.ScheduleTrigger)
	}
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(jobID, store.StreamStdout, 0)
	if err != nil || strings.TrimSpace(string(out)) != "3 new issues" {
		t.Fatalf("job stdout = %q err=%v, want the check output", out, err)
	}
	wait.Until(t, 5*time.Second, "fingerprint stored", func() bool {
		return getSchedule(t, s, "sch-p0").PrecheckFingerprint != ""
	})
	got := getSchedule(t, s, "sch-p0")
	if got.RunCount != 1 || got.LastJobID != jobID {
		t.Fatalf("run_count=%d last_job=%q, want 1 / %s", got.RunCount, got.LastJobID, jobID)
	}
}

// exit 2 skips quietly: schedule.run_skipped{precheck}, no job, no failure.
func TestPrecheckExitTwoSkips(t *testing.T) {
	s := newTestService(t, t.TempDir())
	precheckSchedule(t, s, "sch-p2", []string{"go", "version"}, SchedulePrecheck{Argv: []string{"sh", "-c", "exit 2"}}, nil)
	before := getSchedule(t, s, "sch-p2")
	fireChecked(t, s, "sch-p2")
	waitScheduleEvent(t, s, "sch-p2", EventScheduleRunSkipped, 1)
	if got := skippedReasons(t, s, "sch-p2"); len(got) != 1 || got[0] != ScheduleSkipPrecheck {
		t.Fatalf("skip reasons = %v, want [precheck]", got)
	}
	got := getSchedule(t, s, "sch-p2")
	if n := len(scheduleJobs(t, s, "sch-p2")); n != 0 {
		t.Fatalf("a skipped tick started %d job(s)", n)
	}
	if got.ConsecutiveFailures != 0 || got.RunCount != 0 || got.Enabled != 1 {
		t.Fatalf("failures=%d runs=%d enabled=%d, want 0/0/1", got.ConsecutiveFailures, got.RunCount, got.Enabled)
	}
	// A skipped tick is not a missed one: the schedule row (next_run_at) is untouched.
	if got.NextRunAt != before.NextRunAt || got.PrecheckFingerprint != "" {
		t.Fatalf("a skip changed the row: next %d -> %d, fingerprint %q", before.NextRunAt, got.NextRunAt, got.PrecheckFingerprint)
	}
}

// Any other exit code, and a timeout, are failed runs: schedule.run_failed{precheck} with the
// exit facts (never the output), counted toward the failure streak and the pause.
func TestPrecheckFailureAndTimeoutCountAsFailures(t *testing.T) {
	s := newTestService(t, t.TempDir())
	precheckSchedule(t, s, "sch-pf", []string{"go", "version"},
		SchedulePrecheck{Argv: []string{"sh", "-c", "echo SECRET-LOOKING-OUTPUT; exit 1"}}, func(r *jobstore.ScheduleRecord) { r.MaxFailures = 2 })
	fireChecked(t, s, "sch-pf")
	waitScheduleEvent(t, s, "sch-pf", EventScheduleRunFailed, 1)
	evs, _ := s.meta.ListJobEvents(ScheduleEventScope("sch-pf"), 0)
	var d map[string]any
	_ = json.Unmarshal([]byte(evs[0].Detail), &d)
	if d["reason"] != "precheck" || d["exit_code"] != float64(1) {
		t.Fatalf("run_failed detail = %v", d)
	}
	if strings.Contains(evs[0].Detail, "SECRET-LOOKING-OUTPUT") {
		t.Fatalf("the event carries the check output: %s", evs[0].Detail)
	}
	if got := getSchedule(t, s, "sch-pf"); got.ConsecutiveFailures != 1 || got.PrecheckFingerprint != "" {
		t.Fatalf("failures=%d fingerprint=%q after a failed check", got.ConsecutiveFailures, got.PrecheckFingerprint)
	}

	// A timeout is a failure too, and the second one pauses the schedule.
	setPrecheck(t, s, "sch-pf", SchedulePrecheck{Argv: []string{"sleep", "30"}, TimeoutSec: 1})
	wait.Until(t, 15*time.Second, "previous check finished", func() bool {
		fireOnce := !s.ScheduleDispatcher().pool.inflight.add("sch-pf")
		if !fireOnce {
			s.ScheduleDispatcher().pool.inflight.remove("sch-pf")
		}
		return !fireOnce
	})
	fireChecked(t, s, "sch-pf")
	waitScheduleEvent(t, s, "sch-pf", EventSchedulePaused, 1)
	got := getSchedule(t, s, "sch-pf")
	if got.Enabled != 0 || got.PausedReason != jobstore.SchedulePausedFailures || got.ConsecutiveFailures != 2 {
		t.Fatalf("enabled=%d reason=%q failures=%d, want paused by 2 failures", got.Enabled, got.PausedReason, got.ConsecutiveFailures)
	}
	evs, _ = s.meta.ListJobEvents(ScheduleEventScope("sch-pf"), 0)
	timedOut := false
	for _, ev := range evs {
		if ev.Type == EventScheduleRunFailed && strings.Contains(ev.Detail, `"timed_out":true`) {
			timedOut = true
		}
	}
	if !timedOut {
		t.Fatalf("no run_failed event marks the timeout: %+v", evs)
	}
}

func setPrecheck(t *testing.T, s *Service, id string, pc SchedulePrecheck) {
	t.Helper()
	raw, err := EncodeSchedulePrecheck(&pc)
	if err != nil {
		t.Fatal(err)
	}
	cur := getSchedule(t, s, id)
	if err := s.meta.UpdateSchedule(id, jobstore.ScheduleUpdate{
		Name: cur.Name, ScheduleType: cur.ScheduleType, CronExpr: cur.CronExpr, RequestJSON: cur.RequestJSON,
		Enabled: cur.Enabled, NextRunAt: cur.NextRunAt, Overlap: cur.Overlap, MaxRuns: cur.MaxRuns, MaxFailures: cur.MaxFailures,
		PrecheckJSON: raw, PrecheckFingerprint: cur.PrecheckFingerprint,
	}); err != nil {
		t.Fatal(err)
	}
}

// skip_unchanged: an exit 0 whose output equals the last submitted run's is skipped
// (reason unchanged); a changed output runs and moves the fingerprint; a failed check or a
// refused submit never updates it.
func TestPrecheckSkipUnchangedFingerprint(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	state := filepath.Join(root, "state.txt")
	write := func(v string) {
		if err := os.WriteFile(state, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("v1\n")
	precheckSchedule(t, s, "sch-fp", []string{"go", "version"},
		SchedulePrecheck{Argv: []string{"cat", "state.txt"}, SkipUnchanged: true}, nil)
	waitIdle := func() {
		wait.Until(t, 15*time.Second, "check finished", func() bool {
			ok := s.ScheduleDispatcher().pool.inflight.add("sch-fp")
			if ok {
				s.ScheduleDispatcher().pool.inflight.remove("sch-fp")
			}
			return ok
		})
	}
	endJobs := func() {
		for _, r := range scheduleJobs(t, s, "sch-fp") {
			s.Wait(r.ID)
		}
	}

	fireChecked(t, s, "sch-fp")
	wait.Until(t, 15*time.Second, "first run", func() bool { return getSchedule(t, s, "sch-fp").PrecheckFingerprint != "" })
	waitIdle()
	endJobs()
	fp1 := getSchedule(t, s, "sch-fp").PrecheckFingerprint

	// Same output, only trailing whitespace differs: unchanged.
	write("v1\n\n  ")
	fireChecked(t, s, "sch-fp")
	waitScheduleEvent(t, s, "sch-fp", EventScheduleRunSkipped, 1)
	waitIdle()
	if got := skippedReasons(t, s, "sch-fp"); len(got) != 1 || got[0] != ScheduleSkipUnchanged {
		t.Fatalf("skip reasons = %v, want [unchanged]", got)
	}
	if n := len(scheduleJobs(t, s, "sch-fp")); n != 1 {
		t.Fatalf("jobs = %d after an unchanged tick, want 1", n)
	}

	// A failed check does not touch the fingerprint.
	_ = os.Remove(state)
	fireChecked(t, s, "sch-fp")
	waitScheduleEvent(t, s, "sch-fp", EventScheduleRunFailed, 1)
	waitIdle()
	if got := getSchedule(t, s, "sch-fp").PrecheckFingerprint; got != fp1 {
		t.Fatalf("a failed check changed the fingerprint: %q -> %q", fp1, got)
	}

	// New output: runs, and the fingerprint moves.
	write("v2\n")
	fireChecked(t, s, "sch-fp")
	wait.Until(t, 15*time.Second, "second run", func() bool { return len(scheduleJobs(t, s, "sch-fp")) == 2 })
	waitIdle()
	endJobs()
	wait.Until(t, 5*time.Second, "fingerprint moved", func() bool {
		return getSchedule(t, s, "sch-fp").PrecheckFingerprint != fp1
	})

	// Without skip_unchanged the same output runs again.
	setPrecheck(t, s, "sch-fp", SchedulePrecheck{Argv: []string{"cat", "state.txt"}})
	fireChecked(t, s, "sch-fp")
	wait.Until(t, 15*time.Second, "third run", func() bool { return len(scheduleJobs(t, s, "sch-fp")) == 3 })
	waitIdle()
	endJobs()
}

// The overlap rule is judged BEFORE the check: while the previous run's job is unfinished
// the check command is never started.
func TestPrecheckNotRunWhileOverlapping(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	marker := filepath.Join(root, "check-ran")
	precheckSchedule(t, s, "sch-po", []string{"sleep", "60"},
		SchedulePrecheck{Argv: []string{"sh", "-c", "echo x >> " + marker}}, nil)
	// A running job of the schedule: the previous run.
	prev := jobstore.JobRecord{ID: "prev-run", ProjectKey: "self", Agent: "exec", Runner: "local", Status: StatusRunning,
		Cwd: ".", ResultDir: t.TempDir(), RequestJSON: "{}", StartedAt: 100, ScheduleID: "sch-po", ScheduleTrigger: jobstore.ScheduleTriggerTimer}
	if err := s.meta.UpsertJob(prev); err != nil {
		t.Fatal(err)
	}
	id, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, "sch-po"))
	if err != nil || id != "" {
		t.Fatalf("FireTimer id=%q err=%v", id, err)
	}
	if got := skippedReasons(t, s, "sch-po"); len(got) != 1 || got[0] != ScheduleSkipPreviousRunning {
		t.Fatalf("skip reasons = %v, want [previous_running]", got)
	}
	time.Sleep(300 * time.Millisecond) // nothing should start; this is the "no event" window
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the pre-run check ran although the previous run was still going")
	}
}

// A slow check never blocks the sweep: FireTimer returns at once, a second tick of the same
// schedule while its check is still running is skipped (check_running), and another
// schedule's tick is dispatched right away.
func TestPrecheckDoesNotBlockAndIsSingleFlight(t *testing.T) {
	s := newTestService(t, t.TempDir())
	precheckSchedule(t, s, "sch-slow", []string{"go", "version"}, SchedulePrecheck{Argv: []string{"sleep", "2"}}, nil)
	healthSchedule(t, s, "sch-fast", []string{"go", "version"}, nil)

	start := time.Now()
	fireChecked(t, s, "sch-slow")
	if d := time.Since(start); d > time.Second {
		t.Fatalf("FireTimer took %s with a 2s check in flight", d)
	}
	fireChecked(t, s, "sch-slow") // single flight: skipped, not a second check
	if got := skippedReasons(t, s, "sch-slow"); len(got) != 1 || got[0] != ScheduleSkipCheckRunning {
		t.Fatalf("skip reasons = %v, want [check_running]", got)
	}
	id, err := s.ScheduleDispatcher().FireTimer(nil, getSchedule(t, s, "sch-fast"))
	if err != nil || id == "" {
		t.Fatalf("another schedule's tick: id=%q err=%v, want a job right away", id, err)
	}
	s.Wait(id)
	wait.Until(t, 15*time.Second, "the slow check's run", func() bool { return len(scheduleJobs(t, s, "sch-slow")) == 1 })
	for _, r := range scheduleJobs(t, s, "sch-slow") {
		s.Wait(r.ID)
	}
}

// At most precheckPoolSize checks run at once across schedules.
func TestPrecheckPoolBoundsConcurrency(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	log := filepath.Join(root, "check.log")
	script := "echo start >> " + log + "; sleep 1; echo end >> " + log + "; exit 2"
	const n = 7
	for i := 0; i < n; i++ {
		id := "sch-pool-" + string(rune('a'+i))
		precheckSchedule(t, s, id, []string{"go", "version"}, SchedulePrecheck{Argv: []string{"sh", "-c", script}}, nil)
		fireChecked(t, s, id)
	}
	wait.Until(t, 30*time.Second, "all checks done", func() bool {
		raw, _ := os.ReadFile(log)
		return strings.Count(string(raw), "end") == n
	})
	raw, _ := os.ReadFile(log)
	running, peak := 0, 0
	for _, line := range strings.Fields(string(raw)) {
		switch line {
		case "start":
			running++
			peak = max(peak, running)
		case "end":
			running--
		}
	}
	if peak > precheckPoolSize || peak < 2 {
		t.Fatalf("peak concurrent checks = %d, want between 2 and %d", peak, precheckPoolSize)
	}
}

// The check's environment: gofer's own credentials never reach it — GOFER_* variables, the
// credential deny list and every variable a `*_env` setting names — while the rest of the
// server environment and the schedule's parameters do.
func TestPrecheckEnvironmentStripsGoferCredentials(t *testing.T) {
	t.Setenv("GOFER_TOKEN", "leak-1")
	t.Setenv("GOFER_SOMETHING_ELSE", "leak-2")
	t.Setenv("gofer_lowercase", "leak-3")
	t.Setenv("MY_SERVER_TOKEN_VAR", "leak-4")
	t.Setenv("MY_WORKER_TOKEN_VAR", "leak-5")
	t.Setenv("KEEP_ME", "kept")
	cfg := &config.Config{
		Server: config.ServerConfig{
			TokenEnv: "MY_SERVER_TOKEN_VAR",
			Workers:  map[string]config.WorkerAuthConfig{"w1": {TokenEnv: "MY_WORKER_TOKEN_VAR"}},
		},
	}
	res := runPrecheck(context.Background(), cfg, t.TempDir(),
		SchedulePrecheck{Argv: []string{"sh", "-c", `echo "$GOFER_TOKEN|$GOFER_SOMETHING_ELSE|$gofer_lowercase|$MY_SERVER_TOKEN_VAR|$MY_WORKER_TOKEN_VAR|$KEEP_ME|$PARAM_X"`}},
		map[string]string{"PARAM_X": "p1"})
	if res.ExitCode != 0 || res.Err != nil {
		t.Fatalf("check failed: %+v", res)
	}
	if got, want := strings.TrimSpace(res.Tail), "|||||kept|p1"; got != want {
		t.Fatalf("environment seen by the check = %q, want %q (credentials gone, KEEP_ME and the parameter present)", got, want)
	}
}

// The output fingerprint ignores trailing whitespace (also across writes), and tail keeps the
// last bytes only, flagged as truncated.
func TestTailHashWriter(t *testing.T) {
	sum := func(chunks ...string) string {
		w := newTailHashWriter(8)
		for _, c := range chunks {
			_, _ = w.Write([]byte(c))
		}
		return w.fingerprint()
	}
	if sum("abc") != sum("abc\n") || sum("abc") != sum("ab", "c", " \n", "\t") {
		t.Error("trailing whitespace changed the fingerprint")
	}
	if sum("abc") == sum("ab c") || sum("abc") == sum("abd") || sum("a\nb") != sum("a", "\n", "b") {
		t.Error("interior content must matter, and chunking must not")
	}
	w := newTailHashWriter(4)
	_, _ = w.Write([]byte("0123456789"))
	if string(w.tail) != "6789" || w.total != 10 {
		t.Fatalf("tail=%q total=%d", w.tail, w.total)
	}
}

func TestSchedulePrecheckValidation(t *testing.T) {
	bad := []SchedulePrecheck{
		{}, {Argv: []string{""}}, {Argv: []string{"x"}, TimeoutSec: -1}, {Argv: []string{"x"}, TimeoutSec: 301},
		{Argv: []string{"x\x00y"}},
	}
	for i, pc := range bad {
		if err := pc.Validate(); err == nil {
			t.Errorf("case %d %+v validated", i, pc)
		}
	}
	if err := (SchedulePrecheck{Argv: []string{"x"}, TimeoutSec: 300}).Validate(); err != nil {
		t.Errorf("a valid precheck was refused: %v", err)
	}
	raw, err := EncodeSchedulePrecheck(&SchedulePrecheck{Argv: []string{"a", "b"}, SkipUnchanged: true})
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeSchedulePrecheck(raw)
	if err != nil || back == nil || !back.SkipUnchanged || len(back.Argv) != 2 {
		t.Fatalf("round trip = %+v err=%v", back, err)
	}
	if none, err := DecodeSchedulePrecheck(""); none != nil || err != nil {
		t.Fatalf("empty decodes to %+v / %v", none, err)
	}
}
