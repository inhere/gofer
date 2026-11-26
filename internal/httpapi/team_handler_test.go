package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func teamJSON(t *testing.T, s *Server, token, method, path string, body any, want int) teamView {
	t.Helper()
	status, resp := seatReq(t, s, token, "", method, path, body)
	if status != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, resp)
	}
	var v teamView
	if want == http.StatusOK {
		if err := json.Unmarshal([]byte(resp), &v); err != nil {
			t.Fatalf("decode %s: %v", resp, err)
		}
	}
	return v
}

func TestTeamLifecycleOverHTTP(t *testing.T) {
	s := newCeilingServer(t)
	for _, n := range []string{"lead", "a", "b"} {
		seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": n}, 200)
	}
	registerSeatSession(t, s, "sess-a", "p1", "plain")
	a := seatJSON(t, s, ceilTokPlain, "", "GET", "/v1/seats", nil, 200)
	_ = a
	tm := teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{
		"project_key": "p1", "name": "core", "leader": "lead", "members": []string{"a"}, "brief": "the brief"}, 200)
	if tm.ID == "" || tm.RelayMode != "leader" || tm.RelayRoundLimit != 3 || tm.RelayDailyLimit != 20 || tm.MergeWindowSec != 120 {
		t.Fatalf("created team = %+v", tm)
	}
	if len(tm.Roster) != 2 || tm.Roster[0].Role != "leader" || tm.Roster[0].Name != "lead" || tm.Roster[1].Name != "a" {
		t.Fatalf("roster = %+v", tm.Roster)
	}
	// same name in the project is refused; a seat in two teams is refused
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{"project_key": "p1", "name": "core", "leader": "b"}, 409)
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{"project_key": "p1", "name": "other", "leader": "b", "members": []string{"a"}}, 409)
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{"project_key": "p1", "name": "bad", "leader": "nope"}, 404)
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{"project_key": "p1", "name": "bad", "leader": "b", "relay_mode": "x"}, 400)

	// members
	got := teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams/"+tm.ID+"/members", map[string]any{"seat": "b"}, 200)
	if len(got.Roster) != 3 {
		t.Fatalf("after add: %+v", got.Roster)
	}
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams/"+tm.ID+"/members", map[string]any{"seat": "lead"}, 400)
	got = teamJSON(t, s, ceilTokPlain, "DELETE", "/v1/teams/"+tm.ID+"/members/b", nil, 200)
	if len(got.Roster) != 2 {
		t.Fatalf("after remove: %+v", got.Roster)
	}
	teamJSON(t, s, ceilTokPlain, "DELETE", "/v1/teams/"+tm.ID+"/members/b", nil, 404)

	// patch
	got = teamJSON(t, s, ceilTokPlain, "PATCH", "/v1/teams/"+tm.ID, map[string]any{"brief": "new", "merge_window_sec": 0, "relay_round_limit": 5}, 200)
	if got.Brief != "new" || got.MergeWindowSec != 0 || got.RelayRoundLimit != 5 {
		t.Fatalf("patched = %+v", got)
	}

	// list, get, routes, evals
	status, resp := seatReq(t, s, ceilTokPlain, "", "GET", "/v1/teams?project=p1", nil)
	if status != 200 || !strings.Contains(resp, tm.ID) {
		t.Fatalf("list = %d %s", status, resp)
	}
	for _, p := range []string{"/routes", "/evals"} {
		status, resp = seatReq(t, s, ceilTokPlain, "", "GET", "/v1/teams/"+tm.ID+p, nil)
		if status != 200 || (!strings.Contains(resp, `"routes":[]`) && !strings.Contains(resp, `"evals":null`) && !strings.Contains(resp, `"evals":[]`)) {
			t.Fatalf("GET %s = %d %s", p, status, resp)
		}
	}
	teamJSON(t, s, ceilTokPlain, "GET", "/v1/teams/team-none", nil, 404)

	// archive frees the name
	if v := teamJSON(t, s, ceilTokPlain, "DELETE", "/v1/teams/"+tm.ID, nil, 200); v.ArchivedAt == 0 {
		t.Fatalf("archived = %+v", v)
	}
	teamJSON(t, s, ceilTokPlain, "PATCH", "/v1/teams/"+tm.ID, map[string]any{"brief": "x"}, 409)
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{"project_key": "p1", "name": "core", "leader": "lead", "members": []string{"a"}}, 200)
}

func TestTeamListFollowsCeilingAndPermission(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	inTeam, outTeam := struct{ ID string }{in.teamID}, struct{ ID string }{out.teamID}
	s.setDBGrants(nil)
	_, resp := seatReq(t, s, ceilTokLim, "", "GET", "/v1/teams?all=1", nil)
	if strings.Contains(resp, inTeam.ID) || strings.Contains(resp, outTeam.ID) {
		t.Fatalf("someone else's teams listed without permission: %s", resp)
	}
	// another caller's team needs seat.manage.others on every route
	for _, p := range []string{"", "/routes", "/evals"} {
		if status, resp := seatReq(t, s, ceilTokLim, "", "GET", "/v1/teams/"+inTeam.ID+p, nil); status != http.StatusForbidden || !strings.Contains(resp, "seat.manage.others") {
			t.Fatalf("GET team%s without the permission = %d %s", p, status, resp)
		}
	}
	if status, _ := seatReq(t, s, ceilTokLim, "", "DELETE", "/v1/teams/"+inTeam.ID, nil); status != http.StatusForbidden {
		t.Fatalf("archive without the permission = %d", status)
	}
	grantAll(s)
	_, resp = seatReq(t, s, ceilTokLim, "", "GET", "/v1/teams?all=1", nil)
	if !strings.Contains(resp, inTeam.ID) || strings.Contains(resp, outTeam.ID) {
		t.Fatalf("scope filter: want %s and not %s in %s", inTeam.ID, outTeam.ID, resp)
	}
	// outside the ceiling: reads are 404, creating is refused like an unknown project
	if status, _ := seatReq(t, s, ceilTokLim, "", "GET", "/v1/teams/"+outTeam.ID, nil); status != http.StatusNotFound {
		t.Fatalf("team outside the ceiling = %d, want 404", status)
	}
	if status, _ := seatReq(t, s, ceilTokLim, "", "POST", "/v1/teams", map[string]any{"project_key": "p2", "name": "x", "leader": out.seatID}); status != http.StatusNotFound {
		t.Fatalf("create outside the ceiling = %d, want 404", status)
	}
}
