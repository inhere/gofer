package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seatScheduleStub answers /v1/schedules on top of the seat roster stub and records the create
// bodies, so the tests assert what the CLI put on the wire.
type seatScheduleStub struct {
	seats     *seatStub
	created   []map[string]any
	schedules []map[string]any
}

func (s *seatScheduleStub) start(t *testing.T) {
	t.Helper()
	resetFlagGlobals()
	t.Cleanup(resetFlagGlobals)
	inner := s.seats.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/schedules" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.created = append(s.created, body)
			body["id"] = "sch-1"
			_ = json.NewEncoder(w).Encode(body)
		case r.URL.Path == "/v1/schedules" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"schedules": s.schedules})
		default:
			inner.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	jobConnOpts.server, jobConnOpts.token = srv.URL, "tok"
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
}

func TestSeatScheduleCreatesATargetedSchedule(t *testing.T) {
	stub := &seatScheduleStub{seats: &seatStub{seats: []map[string]any{{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none", "charter_by": "user"}}}}
	stub.start(t)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "schedule", "desk@p1", "--cron", "0 9 * * 1", "--text", "write the weekly report"})
	})
	if code != 0 || len(stub.created) != 1 {
		t.Fatalf("exit=%d created=%+v out=%s", code, stub.created, out)
	}
	body := stub.created[0]
	req, _ := body["request"].(map[string]any)
	if body["target"] != "seat:seat-1" || body["cron"] != "0 9 * * 1" || body["name"] != "desk" || req["prompt"] != "write the weekly report" || req["project_key"] != "p1" {
		t.Fatalf("create body = %+v", body)
	}
	if !strings.Contains(out, "sch-1") || !strings.Contains(out, "schedule rm") {
		t.Errorf("the answer names the schedule and how to remove it: %s", out)
	}
}

func TestSeatScheduleNeedsCronAndText(t *testing.T) {
	stub := &seatScheduleStub{seats: &seatStub{seats: []map[string]any{{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none", "charter_by": "user"}}}}
	stub.start(t)
	var code int
	captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "schedule", "desk@p1", "--cron", "0 9 * * 1"})
	})
	if code == 0 || len(stub.created) != 0 {
		t.Fatalf("a schedule without text must not be created: exit=%d created=%+v", code, stub.created)
	}
}

func TestSeatScheduleLsListsOnlyThisSeat(t *testing.T) {
	stub := &seatScheduleStub{
		seats: &seatStub{seats: []map[string]any{{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none", "charter_by": "user"}}},
		schedules: []map[string]any{
			{"id": "sch-mine", "name": "weekly", "cron": "0 9 * * 1", "enabled": 1, "target": "seat:seat-1", "last_result": "offline", "request": map[string]any{"prompt": "report"}},
			{"id": "sch-other", "name": "other", "cron": "* * * * *", "enabled": 1, "target": "seat:seat-2", "request": map[string]any{"prompt": "x"}},
			{"id": "sch-job", "name": "job", "cron": "* * * * *", "enabled": 1, "request": map[string]any{"prompt": "x"}},
		},
	}
	stub.start(t)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "schedule", "desk@p1", "--ls"})
	})
	if code != 0 || !strings.Contains(out, "sch-mine") || !strings.Contains(out, "offline") || strings.Contains(out, "sch-other") || strings.Contains(out, "sch-job") {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}
