package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func seatScheduleBody(target, project string) map[string]any {
	return map[string]any{
		"name": "weekly", "cron": "0 9 * * 1", "target": target,
		"request": map[string]any{"project_key": project, "prompt": "send the weekly report"},
	}
}

// A schedule may target an existing, live seat of the caller; the view shows the target.
func TestScheduleTargetSeatCreateAndView(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	x := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "desk"}, 200)

	status, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/schedules", seatScheduleBody("seat:"+x.ID, "p1"))
	if status != http.StatusOK {
		t.Fatalf("create = %d %s", status, resp)
	}
	var v scheduleView
	if err := json.Unmarshal([]byte(resp), &v); err != nil {
		t.Fatal(err)
	}
	if v.Target != "seat:"+x.ID || v.Request.Prompt != "send the weekly report" || v.ProjectKey != "p1" {
		t.Fatalf("view = %+v", v)
	}
	rec, _, _ := s.jobs.Meta().GetSchedule(v.ID)
	if rec.Target != "seat:"+x.ID {
		t.Fatalf("stored target = %q", rec.Target)
	}
	// The project may be left out: it is the seat's.
	body := seatScheduleBody("seat:"+x.ID, "")
	delete(body["request"].(map[string]any), "project_key")
	if status, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/schedules", body); status != http.StatusOK || !strings.Contains(resp, `"project_key":"p1"`) {
		t.Fatalf("project defaults to the seat's: %d %s", status, resp)
	}
}

func TestScheduleTargetSeatRefusals(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	x := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "desk"}, 200)
	gone := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "old"}, 200)
	if _, err := s.jobs.Meta().ArchiveSeat(gone.ID, "user"); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		body map[string]any
		want int
	}{
		"unknown seat":      {seatScheduleBody("seat:nope", "p1"), http.StatusBadRequest},
		"archived seat":     {seatScheduleBody("seat:"+gone.ID, "p1"), http.StatusBadRequest},
		"not a seat target": {seatScheduleBody("job:x", "p1"), http.StatusBadRequest},
		"other project":     {seatScheduleBody("seat:"+x.ID, "p2"), http.StatusBadRequest},
	} {
		if status, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/schedules", c.body); status != c.want {
			t.Errorf("%s = %d %s, want %d", name, status, resp, c.want)
		}
	}
	noText := seatScheduleBody("seat:"+x.ID, "p1")
	delete(noText["request"].(map[string]any), "prompt")
	if status, _ := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/schedules", noText); status != http.StatusBadRequest {
		t.Errorf("no text = %d, want 400", status)
	}
	// Nothing was stored by any refusal.
	list, _ := s.jobs.Meta().ListSchedules("", false)
	if len(list) != 0 {
		t.Fatalf("refused creates left %d schedules", len(list))
	}
}

// Someone else's seat needs seat.manage.others, the rule of the seat routes themselves.
func TestScheduleTargetSeatOfAnotherCaller(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1") // the seat is owned by another caller
	s.setDBGrants(grantsFor([]string{"job.submit"}))
	status, resp := seatReq(t, s, ceilTokLim, "", "POST", "/v1/schedules", seatScheduleBody("seat:"+in.seatID, "p1"))
	if status != http.StatusForbidden || !strings.Contains(resp, "seat.manage.others") {
		t.Fatalf("without the permission = %d %s, want 403 naming it", status, resp)
	}
	grantAll(s)
	if status, resp := seatReq(t, s, ceilTokLim, "", "POST", "/v1/schedules", seatScheduleBody("seat:"+in.seatID, "p1")); status != http.StatusOK {
		t.Fatalf("with the permission = %d %s", status, resp)
	}
}

func TestScheduleTargetSeatPatch(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	a := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "a"}, 200)
	b := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "b"}, 200)
	_, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/schedules", seatScheduleBody("seat:"+a.ID, "p1"))
	var v scheduleView
	if err := json.Unmarshal([]byte(resp), &v); err != nil {
		t.Fatal(err)
	}
	if err := s.jobs.Meta().SetScheduleResult(v.ID, jobstore.ScheduleResultOffline); err != nil {
		t.Fatal(err)
	}
	if status, resp := seatReq(t, s, ceilTokPlain, "", "PATCH", "/v1/schedules/"+v.ID, map[string]any{"target": "seat:nope"}); status != http.StatusBadRequest {
		t.Fatalf("patch to an unknown seat = %d %s", status, resp)
	}
	if status, resp := seatReq(t, s, ceilTokPlain, "", "PATCH", "/v1/schedules/"+v.ID, map[string]any{"target": "seat:" + b.ID}); status != http.StatusOK || !strings.Contains(resp, "seat:"+b.ID) {
		t.Fatalf("patch to another seat = %d %s", status, resp)
	}
	rec, _, _ := s.jobs.Meta().GetSchedule(v.ID)
	if rec.Target != "seat:"+b.ID || rec.LastResult != "" {
		t.Fatalf("after retarget: target %q result %q (the old result belongs to the old seat)", rec.Target, rec.LastResult)
	}
}

// A run-now of a seat schedule goes through the injected seat service: with no session in the
// seat the result is offline, and no job is submitted.
func TestScheduleTargetSeatRunNowUsesTheSeatService(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	x := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "desk"}, 200)
	_, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/schedules", seatScheduleBody("seat:"+x.ID, "p1"))
	var v scheduleView
	if err := json.Unmarshal([]byte(resp), &v); err != nil {
		t.Fatal(err)
	}
	status, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/schedules/"+v.ID+"/run-now", nil)
	if status != http.StatusOK || !strings.Contains(resp, `"result":"offline"`) {
		t.Fatalf("run-now = %d %s", status, resp)
	}
	rec, _, _ := s.jobs.Meta().GetSchedule(v.ID)
	if rec.LastResult != jobstore.ScheduleResultOffline {
		t.Fatalf("stored result = %q", rec.LastResult)
	}
	if jobs, _ := s.jobs.Meta().ListJobs(jobstore.ListQuery{}); len(jobs) != 0 {
		t.Fatalf("a seat schedule submits no job: %d", len(jobs))
	}
}
