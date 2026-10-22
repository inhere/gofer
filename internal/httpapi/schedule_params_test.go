package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// paramSchedule returns a create request for an exec schedule declaring two params:
// CLEAN_DAYS (int, default 7) and CLEAN_APPLY (bool, default 0). The stored request
// carries its own env, so the overlay order can be checked end to end.
func paramSchedule(name string) createScheduleReq {
	return createScheduleReq{
		Name: name,
		Cron: "0 5 * * 0",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
			Env: map[string]string{"CLEAN_DAYS": "99", "KEEP": "stored"},
		},
		Params: []job.ScheduleParam{
			{Name: "CLEAN_DAYS", Type: "int", Default: "7", Desc: "keep days"},
			{Name: "CLEAN_APPLY", Type: "bool", Default: "0", Desc: "really delete"},
		},
	}
}

// runScheduleTest parses the job a run-now / trigger submitted and returns its env.
func scheduleJobEnv(t *testing.T, s *Server, jobID string) map[string]string {
	t.Helper()
	rec, ok, err := s.jobs.Meta().GetJob(jobID)
	if err != nil || !ok {
		t.Fatalf("get job %q: ok=%v err=%v", jobID, ok, err)
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		t.Fatalf("decode stored request of %q: %v", jobID, err)
	}
	return req.Env
}

// TestScheduleParamsRunNowOverride (design 2026-10-22 §D1/§D2/§D3): declared params are
// stored on the schedule, a run-now may override them, and the overlay is
// stored env < param default < this run's value.
func TestScheduleParamsRunNowOverride(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, paramSchedule("params-run-now"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created scheduleView
	decode(t, resp, &created)
	if len(created.Params) != 2 || created.Params[0].Name != "CLEAN_DAYS" || created.Params[0].Default != "7" {
		t.Fatalf("params not projected: %+v", created.Params)
	}

	run := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", testToken, struct {
		Params map[string]string `json:"params"`
	}{Params: map[string]string{"CLEAN_APPLY": "1"}})
	if run.StatusCode != http.StatusOK {
		t.Fatalf("run-now status=%d, want 200", run.StatusCode)
	}
	var submitted job.JobResult
	decode(t, run, &submitted)

	env := scheduleJobEnv(t, s, submitted.ID)
	// The param default wins over the stored env of the same name ...
	if env["CLEAN_DAYS"] != "7" {
		t.Fatalf("CLEAN_DAYS = %q, want the param default 7 (stored env was 99)", env["CLEAN_DAYS"])
	}
	// ... the run's own value wins over the default ...
	if env["CLEAN_APPLY"] != "1" {
		t.Fatalf("CLEAN_APPLY = %q, want the run-now override 1", env["CLEAN_APPLY"])
	}
	// ... and the stored env keeps what no param shadows.
	if env["KEEP"] != "stored" {
		t.Fatalf("KEEP = %q, want the stored env value", env["KEEP"])
	}

	// The override is recorded on the schedule's own stream (§D3).
	ev := do(t, s, http.MethodGet, "/v1/schedules/"+created.ID+"/events", testToken, nil)
	if ev.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d, want 200", ev.StatusCode)
	}
	var events struct {
		Events []struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"events"`
	}
	decode(t, ev, &events)
	found := false
	for _, e := range events.Events {
		if e.Type == job.EventScheduleRunNow {
			found = true
			if !strings.Contains(e.Detail, "CLEAN_APPLY") || !strings.Contains(e.Detail, submitted.ID) {
				t.Fatalf("run_now event detail = %q, want the overridden key and the job id", e.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("no %s event recorded: %+v", job.EventScheduleRunNow, events.Events)
	}
}

// TestScheduleRunNowRefusesUndeclaredParam (§D2): run-now is not a way to inject
// arbitrary environment variables — an undeclared key is a 400 naming what IS declared.
func TestScheduleRunNowRefusesUndeclaredParam(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, paramSchedule("params-undeclared"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created scheduleView
	decode(t, resp, &created)

	run := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", testToken, struct {
		Params map[string]string `json:"params"`
	}{Params: map[string]string{"GOFER_JOB_ID": "forged"}})
	if run.StatusCode != http.StatusBadRequest {
		t.Fatalf("undeclared param status=%d, want 400", run.StatusCode)
	}
	body, _ := io.ReadAll(run.Body)
	run.Body.Close()
	if !strings.Contains(string(body), "CLEAN_DAYS") || !strings.Contains(string(body), "CLEAN_APPLY") {
		t.Fatalf("error %q should list the declared params", body)
	}

	// A declared param with a value of the wrong type is refused too.
	bad := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", testToken, struct {
		Params map[string]string `json:"params"`
	}{Params: map[string]string{"CLEAN_APPLY": "yes"}})
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad bool status=%d, want 400", bad.StatusCode)
	}
	bad.Body.Close()
}

// TestScheduleCreateInvalidParams (§D1): the declarations themselves are validated at
// the boundary.
func TestScheduleCreateInvalidParams(t *testing.T) {
	t.Parallel()
	cases := map[string][]job.ScheduleParam{
		"reserved name":  {{Name: "GOFER_JOB_ID", Default: "x"}},
		"bad name":       {{Name: "lower-case", Default: "x"}},
		"duplicate":      {{Name: "A"}, {Name: "A"}},
		"bad int":        {{Name: "A", Type: "int", Default: "seven"}},
		"bad bool":       {{Name: "A", Type: "bool", Default: "true"}},
		"unknown type":   {{Name: "A", Type: "float"}},
		"choice no list": {{Name: "A", Type: "choice", Default: "x"}},
		"choice unlisted": {{Name: "A", Type: "choice", Default: "x",
			Choices: []string{"y"}}},
		"newline value": {{Name: "A", Default: "line1\nline2"}},
	}
	s := newTestServer(t, testToken, false)
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			req := paramSchedule("bad-" + name)
			req.Params = params
			resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400 for params %+v", resp.StatusCode, params)
			}
			resp.Body.Close()
		})
	}
}

// TestSchedulePatchEditableFields (§D4): cron / title / params / enabled / timeout_sec
// are editable, and changing cron recomputes next_run_at from now.
func TestSchedulePatchEditableFields(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, paramSchedule("patch-me"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created scheduleView
	decode(t, resp, &created)

	patch := map[string]any{
		"cron":        "0 6 * * 1",
		"title":       "renamed",
		"timeout_sec": 120,
		"enabled":     false,
		"params": []job.ScheduleParam{
			{Name: "CLEAN_DAYS", Type: "int", Default: "3", Desc: "keep 3 days"},
		},
	}
	patched := do(t, s, http.MethodPatch, "/v1/schedules/"+created.ID, testToken, patch)
	if patched.StatusCode != http.StatusOK {
		t.Fatalf("patch status=%d, want 200", patched.StatusCode)
	}
	var got scheduleView
	decode(t, patched, &got)
	if got.Name != "renamed" || got.Cron != "0 6 * * 1" {
		t.Fatalf("name/cron not patched: %+v", got)
	}
	if got.NextRunAt == 0 || got.NextRunAt == created.NextRunAt {
		t.Fatalf("next_run_at not recomputed: was %d, now %d", created.NextRunAt, got.NextRunAt)
	}
	if got.Enabled != 0 {
		t.Fatalf("enabled = %d, want 0", got.Enabled)
	}
	if got.Request.TimeoutSec != 120 {
		t.Fatalf("timeout_sec = %d, want 120", got.Request.TimeoutSec)
	}
	if len(got.Params) != 1 || got.Params[0].Default != "3" {
		t.Fatalf("params not replaced: %+v", got.Params)
	}
	// The command the schedule runs is untouched by a patch.
	if len(got.Request.Cmd) != 2 || got.Request.Cmd[0] != "go" {
		t.Fatalf("stored command changed by PATCH: %+v", got.Request.Cmd)
	}

	// Re-enabling keeps a next instant that is still ahead of us.
	on := do(t, s, http.MethodPatch, "/v1/schedules/"+created.ID, testToken, map[string]any{"enabled": true})
	if on.StatusCode != http.StatusOK {
		t.Fatalf("enable patch status=%d, want 200", on.StatusCode)
	}
	var back scheduleView
	decode(t, on, &back)
	if back.Enabled != 1 || back.NextRunAt != got.NextRunAt {
		t.Fatalf("re-enable changed the armed instant: enabled=%d next=%d (was %d)", back.Enabled, back.NextRunAt, got.NextRunAt)
	}
}

// TestSchedulePatchForbiddenFields (§D4): the command / agent / runner / project are NOT
// editable — the 400 says the schedule has to be deleted and recreated.
func TestSchedulePatchForbiddenFields(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, paramSchedule("patch-forbidden"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created scheduleView
	decode(t, resp, &created)

	for _, body := range []map[string]any{
		{"cmd": []string{"rm", "-rf", "/"}},
		{"agent": "other"},
		{"runner": "w-docker"},
		{"project": "other-project"},
		{"request": map[string]any{"cmd": []string{"x"}}},
		{"whatever": 1},
	} {
		res := do(t, s, http.MethodPatch, "/v1/schedules/"+created.ID, testToken, body)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("patch %v status=%d, want 400", body, res.StatusCode)
		}
		res.Body.Close()
	}

	// Nothing changed: the stored command is still the one it was created with.
	after := do(t, s, http.MethodGet, "/v1/schedules/"+created.ID, testToken, nil)
	var got scheduleView
	decode(t, after, &got)
	if len(got.Request.Cmd) != 2 || got.Request.Agent != "exec" {
		t.Fatalf("a refused patch changed the schedule: %+v", got.Request)
	}
}

// TestScheduleWebhookUsesParamDefaults (§D2/§D3): an external webhook has only the
// trigger_token, so it cannot choose values — the run uses the declared defaults.
func TestScheduleWebhookUsesParamDefaults(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	req := paramSchedule("webhook-params")
	req.Webhook = true
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created scheduleView
	decode(t, resp, &created)

	run := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/trigger?token="+created.TriggerToken, "", nil)
	if run.StatusCode != http.StatusOK {
		t.Fatalf("trigger status=%d, want 200", run.StatusCode)
	}
	var submitted job.JobResult
	decode(t, run, &submitted)
	env := scheduleJobEnv(t, s, submitted.ID)
	if env["CLEAN_DAYS"] != "7" || env["CLEAN_APPLY"] != "0" {
		t.Fatalf("webhook run env = %+v, want the declared defaults", env)
	}
}
