package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// seatReq sends a request as token, with an optional terminal session id header.
func seatReq(t *testing.T, s *Server, token, sid, method, path string, body any) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sid != "" {
		req.Header.Set(sessionHeader, sid)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func seatJSON(t *testing.T, s *Server, token, sid, method, path string, body any, want int) seatView {
	t.Helper()
	status, resp := seatReq(t, s, token, sid, method, path, body)
	if status != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, resp)
	}
	var v seatView
	if want == http.StatusOK && !strings.Contains(path, "open-plan") {
		if err := json.Unmarshal([]byte(resp), &v); err != nil {
			t.Fatalf("decode %s: %v", resp, err)
		}
	}
	return v
}

func registerSeatSession(t *testing.T, s *Server, sid, project, caller string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: sid, Agent: "claude", ProjectKey: project, Runner: "local", Cwd: s.projects.Config().Storage.Root,
		CallerID: caller, State: jobstore.SessionRunning, StartedAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSeatLifecycleOverHTTP(t *testing.T) {
	s := newCeilingServer(t)
	a := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "dev", "purpose": "pr review", "charter": "be careful"}, 200)
	if a.ID == "" || a.State != "none" || a.CharterBy != "user" {
		t.Fatalf("created seat = %+v", a)
	}
	// same name in the same project is refused, in another project it is fine
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "dev"}, 409)
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p2", "name": "dev"}, 200)

	registerSeatSession(t, s, "sess-a", "p1", "plain")
	registerSeatSession(t, s, "sess-b", "p1", "plain")
	got := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+a.ID+"/bind", map[string]any{"ref": "sess-a"}, 200)
	if got.CurrentRef != "sess-a" || got.State != "running" {
		t.Fatalf("bound seat = %+v", got)
	}
	// a second session cannot take an occupied seat unless it asks to replace
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+a.ID+"/bind", map[string]any{"ref": "sess-b"}, 409)
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+a.ID+"/bind", map[string]any{"ref": "sess-b", "replace": true}, 200)

	// the session view names its seat
	status, resp := seatReq(t, s, ceilTokPlain, "", "GET", "/v1/sessions/sess-b", nil)
	if status != 200 || !strings.Contains(resp, `"seat":{"id":"`+a.ID+`","name":"dev"`) {
		t.Fatalf("session view = %d %s", status, resp)
	}
	status, resp = seatReq(t, s, ceilTokPlain, "", "GET", "/v1/sessions/sess-a", nil)
	if status != 200 || strings.Contains(resp, `"seat"`) {
		t.Fatalf("a session in no seat must not show one: %d %s", status, resp)
	}

	status, resp = seatReq(t, s, ceilTokPlain, "", "GET", "/v1/seats/"+a.ID+"/open-plan", nil)
	if status != 200 || !strings.Contains(resp, `"seat_id":"`+a.ID+`"`) {
		t.Fatalf("open plan = %d %s", status, resp)
	}
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+a.ID+"/unbind", nil, 200)
	if v := seatJSON(t, s, ceilTokPlain, "", "PATCH", "/v1/seats/"+a.ID, map[string]any{"archived": true}, 200); v.ArchivedAt == 0 {
		t.Fatalf("archived seat = %+v", v)
	}
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "dev"}, 200) // archived frees the name
	seatJSON(t, s, ceilTokPlain, "", "DELETE", "/v1/seats/"+a.ID, nil, 200)
	seatJSON(t, s, ceilTokPlain, "", "GET", "/v1/seats/"+a.ID, nil, 404)
}

func TestSeatListFollowsCeilingAndPermission(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")

	// without seat.manage.others a caller sees its own seats only
	s.setDBGrants(nil)
	_, resp := seatReq(t, s, ceilTokLim, "", "GET", "/v1/seats?all=1", nil)
	if strings.Contains(resp, in.seatID) || strings.Contains(resp, out.seatID) {
		t.Fatalf("someone else's seats listed without permission: %s", resp)
	}
	// with it, everything inside the project ceiling and nothing outside
	grantAll(s)
	_, resp = seatReq(t, s, ceilTokLim, "", "GET", "/v1/seats?all=1", nil)
	if !strings.Contains(resp, in.seatID) || strings.Contains(resp, out.seatID) {
		t.Fatalf("scope filter: want %s and not %s in %s", in.seatID, out.seatID, resp)
	}
	// creating a seat in a project outside the ceiling is refused like an unknown one
	if status, _ := seatReq(t, s, ceilTokLim, "", "POST", "/v1/seats", map[string]any{"project_key": "p2", "name": "x"}); status != http.StatusNotFound {
		t.Fatalf("create outside the ceiling = %d, want 404", status)
	}
}

func TestSeatOfAnotherCallerNeedsPermission(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1") // owned by another caller
	s.setDBGrants(nil)
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/seats/" + in.seatID}, {"PATCH", "/v1/seats/" + in.seatID}, {"DELETE", "/v1/seats/" + in.seatID},
		{"POST", "/v1/seats/" + in.seatID + "/bind"}, {"POST", "/v1/seats/" + in.seatID + "/unbind"},
		{"GET", "/v1/seats/" + in.seatID + "/open-plan"},
	} {
		body := any(nil)
		if c.method == "PATCH" || strings.HasSuffix(c.path, "/bind") {
			body = map[string]any{"purpose": "x", "ref": "r"}
		}
		if status, resp := seatReq(t, s, ceilTokLim, "", c.method, c.path, body); status != http.StatusForbidden || !strings.Contains(resp, "seat.manage.others") {
			t.Fatalf("%s %s without the permission = %d %s, want 403 naming it", c.method, c.path, status, resp)
		}
	}
	grantAll(s)
	seatJSON(t, s, ceilTokLim, "", "GET", "/v1/seats/"+in.seatID, nil, 200)
}

func TestSeatCharterByTracksTheWriter(t *testing.T) {
	s := newCeilingServer(t)
	x := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "dev", "charter": "v1"}, 200)
	registerSeatSession(t, s, "sess-w", "p1", "plain")

	// a request that names the caller's live session is recorded as that session
	v := seatJSON(t, s, ceilTokPlain, "sess-w", "PATCH", "/v1/seats/"+x.ID, map[string]any{"charter": "v2 by session"}, 200)
	if v.CharterBy != "session:sess-w" || v.Charter != "v2 by session" {
		t.Fatalf("seat = %+v", v)
	}
	// a session id that is not registered (or ended) does not count
	v = seatJSON(t, s, ceilTokPlain, "no-such-session", "PATCH", "/v1/seats/"+x.ID, map[string]any{"purpose": "by a person"}, 200)
	if v.CharterBy != "user" {
		t.Fatalf("an unknown session id must not be trusted: %+v", v)
	}
	hist, err := s.jobs.Meta().ListSeatCharterHistory(x.ID)
	if err != nil {
		t.Fatal(err)
	}
	var by []string
	for _, h := range hist {
		by = append(by, h.By)
	}
	if strings.Join(by, ",") != "user,session:sess-w,user" {
		t.Fatalf("charter history writers = %v", by)
	}
}

func TestSeatCandidatesOverHTTP(t *testing.T) {
	s := newCeilingServer(t)
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "dev"}, 200)
	registerSeatSession(t, s, "sess-c", "p1", "plain")
	status, resp := seatReq(t, s, ceilTokPlain, "", "GET", "/v1/sessions/sess-c/seat-candidates", nil)
	if status != 200 || !strings.Contains(resp, `"name":"dev"`) {
		t.Fatalf("candidates = %d %s", status, resp)
	}
}

// seedEndedACPSeat creates an acp seat of caller whose job has ended.
func seedEndedACPSeat(t *testing.T, s *Server, caller string) jobstore.Seat {
	t.Helper()
	st := s.jobs.Meta()
	now := time.Now().Unix()
	if err := st.UpsertJob(jobstore.JobRecord{
		ID: "acp-old-" + caller, ProjectKey: "p1", Agent: "exec", Runner: "local", Status: "done", SessionID: "acp-sess-" + caller,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now, CallerID: caller,
	}); err != nil {
		t.Fatal(err)
	}
	x, err := st.CreateSeat(jobstore.Seat{CallerID: caller, ProjectKey: "p1", Name: "acp-" + caller, Kind: jobstore.SeatKindACP, Agent: "exec"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BindSeat(x.ID, "acp-old-"+caller, false, "user"); err != nil {
		t.Fatal(err)
	}
	return x
}

func jobCount(t *testing.T, s *Server) int {
	t.Helper()
	rows, err := s.jobs.Meta().ListJobs(jobstore.ListQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestSeatOpenPlanIsReadOnly(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	x := seedEndedACPSeat(t, s, "plain")
	before := jobCount(t, s)
	for _, path := range []string{"/v1/seats/" + x.ID + "/open-plan", "/v1/seats/" + x.ID + "/open-plan?restart=1"} {
		status, resp := seatReq(t, s, ceilTokPlain, "", "GET", path, nil)
		if status != 200 || !strings.Contains(resp, `"needs_restart":true`) || strings.Contains(resp, `"restarted"`) {
			t.Fatalf("GET %s = %d %s", path, status, resp)
		}
	}
	if after := jobCount(t, s); after != before {
		t.Fatalf("GET open-plan submitted a job: %d -> %d", before, after)
	}
	got, _, _ := s.jobs.Meta().GetSeat(x.ID)
	if got.CurrentRef != "acp-old-plain" {
		t.Fatalf("GET open-plan moved the seat to %q", got.CurrentRef)
	}
}

func TestSeatRestartNeedsJobSubmit(t *testing.T) {
	s := newCeilingServer(t)
	plain := seedEndedACPSeat(t, s, "plain")
	ro := seedEndedACPSeat(t, s, "ro")
	before := jobCount(t, s)

	// the seat's owner without job.submit is refused
	s.setDBGrants(nil)
	status, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+plain.ID+"/restart", nil)
	if status != http.StatusForbidden || !strings.Contains(resp, "job.submit") {
		t.Fatalf("restart without job.submit = %d %s, want 403 naming it", status, resp)
	}
	// a read-only caller is refused even with every permission
	grantAll(s)
	status, resp = seatReq(t, s, ceilTokRO, "", "POST", "/v1/seats/"+ro.ID+"/restart", nil)
	if status != http.StatusForbidden || !strings.Contains(resp, "read-only") {
		t.Fatalf("restart by a read-only caller = %d %s", status, resp)
	}
	if after := jobCount(t, s); after != before {
		t.Fatalf("a refused restart submitted a job: %d -> %d", before, after)
	}
	// with job.submit the request reaches the restart (the fixture's exec agent is no ACP agent,
	// so the submit itself is refused later, but never with a permission error)
	status, resp = seatReq(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+plain.ID+"/restart", nil)
	if status == http.StatusForbidden {
		t.Fatalf("restart with job.submit refused: %d %s", status, resp)
	}
	// a seat with nothing to restart is a client error
	vacant, err := s.jobs.Meta().CreateSeat(jobstore.Seat{CallerID: "plain", ProjectKey: "p1", Name: "vacant"})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+vacant.ID+"/restart", nil); status != http.StatusBadRequest {
		t.Fatalf("restart of a vacant seat = %d, want 400", status)
	}
}
