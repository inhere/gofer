package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func healthScheduleReq(name string, cmd []string) createScheduleReq {
	return createScheduleReq{
		Name: name, Cron: "*/5 * * * *",
		Request: job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: cmd, Cwd: ".", TimeoutSec: 60},
	}
}

func createHealthSchedule(t *testing.T, s *Server, req createScheduleReq) scheduleView {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var v scheduleView
	decode(t, resp, &v)
	return v
}

// A new schedule defaults to overlap=skip and max_failures=3 with no run limit; explicit
// values are stored; bad values are a 400.
func TestScheduleCreateRunHealthDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	def := createHealthSchedule(t, s, healthScheduleReq("defaults", []string{"go", "version"}))
	if def.Overlap != "skip" || def.MaxFailures != 3 || def.MaxRuns != 0 || def.RunCount != 0 || def.ConsecutiveFailures != 0 || def.PausedReason != "" {
		t.Fatalf("defaults = %+v", def)
	}

	runs, fails := 5, 0
	req := healthScheduleReq("explicit", []string{"go", "version"})
	req.Overlap, req.MaxRuns, req.MaxFailures = "allow", &runs, &fails
	got := createHealthSchedule(t, s, req)
	if got.Overlap != "allow" || got.MaxRuns != 5 || got.MaxFailures != 0 {
		t.Fatalf("explicit = %+v", got)
	}

	neg := -1
	for name, mut := range map[string]func(*createScheduleReq){
		"overlap":      func(r *createScheduleReq) { r.Overlap = "queue" },
		"max_runs":     func(r *createScheduleReq) { r.MaxRuns = &neg },
		"max_failures": func(r *createScheduleReq) { r.MaxFailures = &neg },
	} {
		bad := healthScheduleReq("bad-"+name, []string{"go", "version"})
		mut(&bad)
		resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status=%d, want 400", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// PATCH edits the three knobs; enabling a paused schedule clears its pause bookkeeping.
func TestSchedulePatchRunHealthAndEnableClearsPause(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	created := createHealthSchedule(t, s, healthScheduleReq("patch-health", []string{"go", "version"}))
	meta := s.jobs.Meta()
	if _, _, _, err := meta.IncScheduleFailure(created.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := meta.PauseSchedule(created.ID, jobstore.SchedulePausedFailures); err != nil || !ok {
		t.Fatalf("PauseSchedule ok=%v err=%v", ok, err)
	}

	resp := do(t, s, http.MethodPatch, "/v1/schedules/"+created.ID, testToken,
		map[string]any{"overlap": "allow", "max_runs": 9, "max_failures": 5})
	var got scheduleView
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status=%d", resp.StatusCode)
	}
	decode(t, resp, &got)
	if got.Overlap != "allow" || got.MaxRuns != 9 || got.MaxFailures != 5 {
		t.Fatalf("patched = %+v", got)
	}
	if got.Enabled != 0 || got.PausedReason != "failures" || got.ConsecutiveFailures != 1 {
		t.Fatalf("a patch that leaves the schedule off must keep the pause: %+v", got)
	}

	on := do(t, s, http.MethodPatch, "/v1/schedules/"+created.ID, testToken, map[string]any{"enabled": true})
	var enabled scheduleView
	decode(t, on, &enabled)
	if enabled.Enabled != 1 || enabled.PausedReason != "" || enabled.ConsecutiveFailures != 0 {
		t.Fatalf("enabling must clear the pause: enabled=%d reason=%q failures=%d", enabled.Enabled, enabled.PausedReason, enabled.ConsecutiveFailures)
	}

	for _, body := range []map[string]any{{"overlap": "queue"}, {"max_runs": -1}, {"max_failures": -2}} {
		bad := do(t, s, http.MethodPatch, "/v1/schedules/"+created.ID, testToken, body)
		if bad.StatusCode != http.StatusBadRequest {
			t.Fatalf("patch %v: status=%d, want 400", body, bad.StatusCode)
		}
		bad.Body.Close()
	}
}

// run-now and the webhook go through the overlap rule too: while a previous run of the
// schedule is unfinished they answer 409 and write schedule.run_skipped; neither counts
// as a timer run.
func TestScheduleRunNowAndWebhookHonourOverlap(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	req := healthScheduleReq("overlap-now", []string{"go", "version"})
	created := createHealthSchedule(t, s, req)
	meta := s.jobs.Meta()

	// An unfinished job of this schedule (a running exec job started earlier).
	if err := meta.UpsertJob(jobstore.JobRecord{
		ID: "busy-1", ProjectKey: "self", Agent: "exec", Runner: "local", Status: "running", Cwd: ".",
		ResultDir: "/tmp/busy-1", RequestJSON: "{}", StartedAt: 100, ScheduleID: created.ID,
		ScheduleTrigger: jobstore.ScheduleTriggerTimer,
	}); err != nil {
		t.Fatal(err)
	}
	resp := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("run-now status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	evs, err := meta.ListJobEvents(job.ScheduleEventScope(created.ID), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != job.EventScheduleRunSkipped {
		t.Fatalf("events = %+v, want one %s", evs, job.EventScheduleRunSkipped)
	}
	var d struct {
		Reason string `json:"reason"`
		JobID  string `json:"job_id"`
	}
	_ = json.Unmarshal([]byte(evs[0].Detail), &d)
	if d.Reason != "previous_running" || d.JobID != "busy-1" {
		t.Fatalf("skip detail = %+v", d)
	}

	// With overlap=allow the same run-now goes through, stamped run_now, not counted.
	patched := do(t, s, http.MethodPatch, "/v1/schedules/"+created.ID, testToken, map[string]any{"overlap": "allow"})
	patched.Body.Close()
	ok := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", testToken, nil)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("run-now with overlap=allow status=%d, want 200", ok.StatusCode)
	}
	var res job.JobResult
	decode(t, ok, &res)
	s.jobs.Wait(res.ID)
	rec, found, _ := meta.GetJob(res.ID)
	if !found || rec.ScheduleID != created.ID || rec.ScheduleTrigger != jobstore.ScheduleTriggerRunNow {
		t.Fatalf("run-now job = %+v", rec)
	}
	after, _, _ := meta.GetSchedule(created.ID)
	if after.RunCount != 0 {
		t.Fatalf("run_count = %d after a run-now, want 0", after.RunCount)
	}
}
