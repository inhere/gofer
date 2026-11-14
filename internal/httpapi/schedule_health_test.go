package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
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
	if def.Context != "on" {
		t.Fatalf("a new schedule's context = %q, want on", def.Context)
	}
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

// precheckServer has an ordinary caller (alice: job.submit, no schedule.precheck — it is not
// seeded), a root administrator and a peer runner a schedule may name.
func precheckServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerCfgWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "root", Token: "tok-root", CanAdmin: true},
		},
	}, func(cfg *config.Config) {
		cfg.Runners = map[string]config.RunnerConfig{"peer": {Type: "peer-http", BaseURL: "http://peer.example.test"}}
		p := cfg.Projects["self"]
		p.AllowedRunners = []string{"local", "peer"}
		cfg.Projects["self"] = p
	})
}

func precheckCreate(name, runner string, pc *job.SchedulePrecheck) createScheduleReq {
	return createScheduleReq{
		Name: name, Cron: "*/5 * * * *", Precheck: pc,
		Request: job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: runner, Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30},
	}
}

// Setting a pre-run check needs schedule.precheck (a caller holding only job.submit gets a 403
// naming it, and nothing is stored); creating without one still works; a root administrator
// may set it, and the view shows it.
func TestSchedulePrecheckCreatePermission(t *testing.T) {
	t.Parallel()
	s := precheckServer(t)
	pc := &job.SchedulePrecheck{Argv: []string{"true"}, TimeoutSec: 30, SkipUnchanged: true}

	resp := do(t, s, http.MethodPost, "/v1/schedules", "tok-alice", precheckCreate("alice-pre", "local", pc))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("create with precheck as alice: status=%d, want 403 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	body := readBody(t, resp)
	if !strings.Contains(body, authz.PermSchedulePrecheck) {
		t.Fatalf("403 body %q must name %s", body, authz.PermSchedulePrecheck)
	}
	if list, _ := s.jobs.Meta().ListSchedules("", false); len(list) != 0 {
		t.Fatalf("a refused create stored %d schedule(s)", len(list))
	}

	plain := do(t, s, http.MethodPost, "/v1/schedules", "tok-alice", precheckCreate("alice-plain", "local", nil))
	if plain.StatusCode != http.StatusOK {
		t.Fatalf("create without precheck as alice: status=%d", plain.StatusCode)
	}
	plain.Body.Close()

	ok := do(t, s, http.MethodPost, "/v1/schedules", "tok-root", precheckCreate("root-pre", "local", pc))
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("create with precheck as root: status=%d (body %s)", ok.StatusCode, readBody(t, ok))
	}
	var v scheduleView
	decode(t, ok, &v)
	if v.Precheck == nil || len(v.Precheck.Argv) != 1 || v.Precheck.TimeoutSec != 30 || !v.Precheck.SkipUnchanged {
		t.Fatalf("view precheck = %+v", v.Precheck)
	}
}

// A pre-run check runs on the server, so a schedule on a remote runner cannot have one (400);
// the check's own shape is validated (empty argv is "no check", a timeout above 300 is 400).
func TestSchedulePrecheckRunnerAndShape(t *testing.T) {
	t.Parallel()
	s := precheckServer(t)
	remote := do(t, s, http.MethodPost, "/v1/schedules", "tok-root", precheckCreate("remote", "peer", &job.SchedulePrecheck{Argv: []string{"true"}}))
	if remote.StatusCode != http.StatusBadRequest {
		t.Fatalf("precheck on a peer runner: status=%d, want 400 (body %s)", remote.StatusCode, readBody(t, remote))
	}
	remote.Body.Close()
	noCheck := do(t, s, http.MethodPost, "/v1/schedules", "tok-root", precheckCreate("remote-plain", "peer", nil))
	if noCheck.StatusCode != http.StatusOK {
		t.Fatalf("a peer-runner schedule without a precheck: status=%d, want 200 (body %s)", noCheck.StatusCode, readBody(t, noCheck))
	}
	noCheck.Body.Close()
	long := do(t, s, http.MethodPost, "/v1/schedules", "tok-root", precheckCreate("long", "local", &job.SchedulePrecheck{Argv: []string{"true"}, TimeoutSec: 301}))
	if long.StatusCode != http.StatusBadRequest {
		t.Fatalf("timeout 301: status=%d, want 400", long.StatusCode)
	}
	long.Body.Close()
}

// PATCH: setting needs schedule.precheck; clearing (null) needs nothing, even for a caller
// without the permission; the same argv keeps the output fingerprint, a different one clears it.
func TestSchedulePrecheckPatch(t *testing.T) {
	t.Parallel()
	s := precheckServer(t)
	created := do(t, s, http.MethodPost, "/v1/schedules", "tok-root", precheckCreate("patch-pre", "local",
		&job.SchedulePrecheck{Argv: []string{"check", "--all"}, SkipUnchanged: true}))
	var v scheduleView
	decode(t, created, &v)
	meta := s.jobs.Meta()
	if err := meta.SetSchedulePrecheckFingerprint(v.ID, "abc123"); err != nil {
		t.Fatal(err)
	}
	// alice owns nothing here, but holds schedule.manage.others by default: she may edit, yet
	// not set a precheck.
	denied := do(t, s, http.MethodPatch, "/v1/schedules/"+v.ID, "tok-alice",
		map[string]any{"precheck": map[string]any{"argv": []string{"other"}}})
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("patch precheck as alice: status=%d, want 403 (body %s)", denied.StatusCode, readBody(t, denied))
	}
	denied.Body.Close()

	// Same command, other timeout: fingerprint kept.
	same := do(t, s, http.MethodPatch, "/v1/schedules/"+v.ID, "tok-root",
		map[string]any{"precheck": map[string]any{"argv": []string{"check", "--all"}, "timeout_sec": 90, "skip_unchanged": true}})
	if same.StatusCode != http.StatusOK {
		t.Fatalf("patch same argv: status=%d", same.StatusCode)
	}
	same.Body.Close()
	if rec, _, _ := meta.GetSchedule(v.ID); rec.PrecheckFingerprint != "abc123" {
		t.Fatalf("fingerprint = %q after a patch with the same argv, want it kept", rec.PrecheckFingerprint)
	}
	// Different command: fingerprint cleared.
	diff := do(t, s, http.MethodPatch, "/v1/schedules/"+v.ID, "tok-root",
		map[string]any{"precheck": map[string]any{"argv": []string{"check", "--new"}}})
	diff.Body.Close()
	rec, _, _ := meta.GetSchedule(v.ID)
	if rec.PrecheckFingerprint != "" || !strings.Contains(rec.PrecheckJSON, "--new") {
		t.Fatalf("after changing argv: fingerprint=%q json=%s", rec.PrecheckFingerprint, rec.PrecheckJSON)
	}

	// Clearing needs no permission, and leaves the rest of the schedule alone.
	cleared := do(t, s, http.MethodPatch, "/v1/schedules/"+v.ID, "tok-alice", map[string]any{"precheck": nil})
	if cleared.StatusCode != http.StatusOK {
		t.Fatalf("clear precheck as alice: status=%d (body %s)", cleared.StatusCode, readBody(t, cleared))
	}
	var after scheduleView
	decode(t, cleared, &after)
	if after.Precheck != nil {
		t.Fatalf("precheck still set after clearing: %+v", after.Precheck)
	}
	if rec, _, _ := meta.GetSchedule(v.ID); rec.PrecheckJSON != "" || rec.PrecheckFingerprint != "" {
		t.Fatalf("row after clear: json=%q fingerprint=%q", rec.PrecheckJSON, rec.PrecheckFingerprint)
	}
}

// context: omitted = on for a new schedule; off is stored; anything else is a 400; PATCH
// switches it and refuses a bad value.
func TestScheduleContextCreateAndPatch(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	off := healthScheduleReq("ctx-off", []string{"go", "version"})
	off.Context = "off"
	v := createHealthSchedule(t, s, off)
	if v.Context != "off" {
		t.Fatalf("created context = %q, want off", v.Context)
	}
	bad := healthScheduleReq("ctx-bad", []string{"go", "version"})
	bad.Context = "maybe"
	if resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("context=maybe: status=%d, want 400", resp.StatusCode)
	}

	resp := do(t, s, http.MethodPatch, "/v1/schedules/"+v.ID, testToken, map[string]any{"context": "on"})
	var after scheduleView
	decode(t, resp, &after)
	if after.Context != "on" {
		t.Fatalf("patched context = %q, want on", after.Context)
	}
	for _, body := range []map[string]any{{"context": "maybe"}, {"context": ""}} {
		r := do(t, s, http.MethodPatch, "/v1/schedules/"+v.ID, testToken, body)
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("patch %v: status=%d, want 400", body, r.StatusCode)
		}
		r.Body.Close()
	}
	// A patch that does not mention it leaves it alone.
	keep := do(t, s, http.MethodPatch, "/v1/schedules/"+v.ID, testToken, map[string]any{"title": "renamed"})
	var kept scheduleView
	decode(t, keep, &kept)
	if kept.Context != "on" {
		t.Fatalf("context = %q after an unrelated patch, want on", kept.Context)
	}
}
