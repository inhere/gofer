package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
)

func TestScheduleLifecycle(t *testing.T) {
	s := newTestServer(t, testToken, false)

	createResp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name: "nightly",
		Cron: "*/5 * * * *",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
	})
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", createResp.StatusCode)
	}
	var created scheduleView
	decode(t, createResp, &created)
	if !strings.HasPrefix(created.ID, "sch-") {
		t.Fatalf("schedule id %q does not have sch- prefix", created.ID)
	}
	if created.Name != "nightly" || created.Type != "cron" || created.Cron != "*/5 * * * *" {
		t.Fatalf("unexpected created schedule: %+v", created)
	}
	if created.Enabled != 1 || created.CatchUp != 1 {
		t.Fatalf("enabled/catch_up defaults not applied: %+v", created)
	}
	if created.ProjectKey != "self" || created.Request.ProjectKey != "self" {
		t.Fatalf("project not projected: %+v", created)
	}
	if created.NextRunAt == 0 {
		t.Fatalf("next_run_at not set: %+v", created)
	}

	listResp := do(t, s, http.MethodGet, "/v1/schedules?project=self", testToken, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", listResp.StatusCode)
	}
	var list struct {
		Schedules []scheduleView `json:"schedules"`
	}
	decode(t, listResp, &list)
	if len(list.Schedules) != 1 || list.Schedules[0].ID != created.ID {
		t.Fatalf("unexpected list: %+v", list.Schedules)
	}

	getResp := do(t, s, http.MethodGet, "/v1/schedules/"+created.ID, testToken, nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d, want 200", getResp.StatusCode)
	}
	var got scheduleView
	decode(t, getResp, &got)
	if got.ID != created.ID || got.Request.Agent != "exec" {
		t.Fatalf("unexpected get schedule: %+v", got)
	}

	delResp := do(t, s, http.MethodDelete, "/v1/schedules/"+created.ID, testToken, nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d, want 200", delResp.StatusCode)
	}
	afterDelete := do(t, s, http.MethodGet, "/v1/schedules/"+created.ID, testToken, nil)
	if afterDelete.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted status=%d, want 404", afterDelete.StatusCode)
	}
	afterDelete.Body.Close()
}

func TestScheduleCreateInvalidCron(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name: "bad",
		Cron: "not cron",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cron status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestScheduleCreateInvalidAgent(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name: "bad-agent",
		Cron: "*/5 * * * *",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "claude", Runner: "local",
			Prompt: "hi", Cwd: ".", TimeoutSec: 30,
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid agent status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestScheduleCreateOnceDelay(t *testing.T) {
	s := newTestServer(t, testToken, false)
	before := time.Now().Unix()
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name:     "once-delay",
		Type:     "once",
		DelaySec: 30,
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("once delay status=%d, want 200", resp.StatusCode)
	}
	var created scheduleView
	decode(t, resp, &created)
	if created.Type != "once" || created.Cron != "" {
		t.Fatalf("unexpected once delay schedule: %+v", created)
	}
	if created.NextRunAt < before+30 || created.NextRunAt > time.Now().Unix()+30 {
		t.Fatalf("next_run_at=%d outside delay window", created.NextRunAt)
	}
}

func TestScheduleCreateOnceRunAt(t *testing.T) {
	s := newTestServer(t, testToken, false)
	runAt := time.Now().Unix() + 60
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name:  "once-at",
		Type:  "once",
		RunAt: runAt,
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("once run_at status=%d, want 200", resp.StatusCode)
	}
	var created scheduleView
	decode(t, resp, &created)
	if created.Type != "once" || created.NextRunAt != runAt || created.Cron != "" {
		t.Fatalf("unexpected once run_at schedule: %+v", created)
	}
}

func TestScheduleCreateOnceInvalid(t *testing.T) {
	s := newTestServer(t, testToken, false)
	base := createScheduleReq{
		Name: "bad-once",
		Type: "once",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
	}
	cases := []struct {
		name string
		mut  func(*createScheduleReq)
	}{
		{name: "with cron", mut: func(r *createScheduleReq) { r.Cron = "*/5 * * * *"; r.DelaySec = 30 }},
		{name: "missing time", mut: func(r *createScheduleReq) {}},
		{name: "past run_at", mut: func(r *createScheduleReq) { r.RunAt = time.Now().Unix() - 1 }},
	}
	for _, tc := range cases {
		req := base
		tc.mut(&req)
		resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status=%d, want 400", tc.name, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestScheduleEnableDisable(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := createTestSchedule(t, s)

	disable := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/disable", testToken, nil)
	if disable.StatusCode != http.StatusOK {
		t.Fatalf("disable status=%d, want 200", disable.StatusCode)
	}
	var disabled scheduleView
	decode(t, disable, &disabled)
	if disabled.Enabled != 0 {
		t.Fatalf("disabled enabled=%d, want 0", disabled.Enabled)
	}

	enable := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/enable", testToken, nil)
	if enable.StatusCode != http.StatusOK {
		t.Fatalf("enable status=%d, want 200", enable.StatusCode)
	}
	var enabled scheduleView
	decode(t, enable, &enabled)
	if enabled.Enabled != 1 {
		t.Fatalf("enabled enabled=%d, want 1", enabled.Enabled)
	}
}

func TestScheduleRunNowSubmitsJobWithoutChangingNextRun(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := createTestSchedule(t, s)

	run := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", testToken, nil)
	if run.StatusCode != http.StatusOK {
		t.Fatalf("run-now status=%d, want 200", run.StatusCode)
	}
	var res job.JobResult
	decode(t, run, &res)
	if res.ID == "" {
		t.Fatalf("run-now returned empty job id: %+v", res)
	}
	if res.Channel != "cron" {
		t.Fatalf("run-now channel=%q, want cron", res.Channel)
	}
	waitDone(t, s, res.ID)

	getResp := do(t, s, http.MethodGet, "/v1/schedules/"+created.ID, testToken, nil)
	var after scheduleView
	decode(t, getResp, &after)
	if after.NextRunAt != created.NextRunAt {
		t.Fatalf("next_run_at changed after run-now: before=%d after=%d", created.NextRunAt, after.NextRunAt)
	}
}

func createTestSchedule(t *testing.T, s *Server) scheduleView {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name: "test",
		Cron: "*/5 * * * *",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create schedule status=%d, want 200", resp.StatusCode)
	}
	var out scheduleView
	decode(t, resp, &out)
	return out
}
