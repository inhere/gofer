package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// leaderWorld is two teams in p1: core (leader seat sitting a JOB, member a) and core2 (leader
// seat sitting a terminal session, member b).
func leaderWorld(t *testing.T) (s *Server, jobTok, memberJobTok, otherJobTok string) {
	t.Helper()
	s = newCeilingServer(t)
	grantAll(s)
	ids := map[string]string{}
	for _, n := range []string{"lead", "a", "lead2", "b"} {
		ids[n] = seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": n}, 200).ID
	}
	jobTok = seedCredentialJob(t, s, "job-lead", "p1", "plain")
	memberJobTok = seedCredentialJob(t, s, "job-a", "p1", "plain")
	otherJobTok = seedCredentialJob(t, s, "job-x", "p1", "plain")
	registerSeatSession(t, s, "sess-lead2", "p1", "plain")
	registerSeatSession(t, s, "sess-b", "p1", "plain")
	for seat, ref := range map[string]string{"lead": "job-lead", "a": "job-a", "lead2": "sess-lead2", "b": "sess-b"} {
		if _, err := s.jobs.Meta().BindSeat(ids[seat], ref, false, "user"); err != nil {
			t.Fatal(err)
		}
	}
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{"project_key": "p1", "name": "core", "leader": "lead", "members": []string{"a"}, "brief": "brief one"}, 200)
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{"project_key": "p1", "name": "core2", "leader": "lead2", "members": []string{"b"}}, 200)
	return s, jobTok, memberJobTok, otherJobTok
}

func evalCall(t *testing.T, s *Server, token, sid string, body any, want int) string {
	t.Helper()
	status, resp := seatReq(t, s, token, sid, http.MethodPost, "/v1/teams/eval", body)
	if status != want {
		t.Fatalf("POST /v1/teams/eval = %d, want %d: %s", status, want, resp)
	}
	return resp
}

// Only the leader's own credential may record an eval: the job sitting in the leader seat, or the
// terminal session sitting in it. A member's job, another job, a person's token and a person's
// token naming a member session are refused (403).
func TestTeamEvalAuthz(t *testing.T) {
	s, jobTok, memberTok, otherTok := leaderWorld(t)
	ok := map[string]any{"outcome": "dispatched", "reason": "sent the task"}

	// the leader job's credential (ACP end)
	resp := evalCall(t, s, jobTok, "", ok, 200)
	var e teamEvalView
	if err := json.Unmarshal([]byte(resp), &e); err != nil || e.Outcome != "dispatched" || e.LeaderRef != "job-lead" || e.Reason != "sent the task" {
		t.Fatalf("eval = %+v %v", e, err)
	}
	// the leader terminal session (terminal end): a person's token plus the session header
	evalCall(t, s, ceilTokPlain, "sess-lead2", map[string]any{"outcome": "failed", "reason": "stuck"}, 200)

	// refused
	evalCall(t, s, memberTok, "", ok, 403)               // a member's job
	evalCall(t, s, otherTok, "", ok, 403)                // a job in no seat
	evalCall(t, s, ceilTokPlain, "", ok, 403)            // a person's token alone
	evalCall(t, s, ceilTokPlain, "sess-b", ok, 403)      // a person naming a member's session
	evalCall(t, s, ceilTokPlain, "sess-nobody", ok, 403) // an unknown session
	evalCall(t, s, jobTok, "", map[string]any{"outcome": "x"}, 400)
	evalCall(t, s, jobTok, "", map[string]any{"outcome": "missing_eval"}, 400)

	// both evaluations are in their teams' lists, and nothing was recorded for the refusals
	status, list := seatReq(t, s, ceilTokPlain, "", "GET", "/v1/teams?project=p1", nil)
	if status != 200 {
		t.Fatalf("list = %d", status)
	}
	var teams struct {
		Teams []teamView `json:"teams"`
	}
	_ = json.Unmarshal([]byte(list), &teams)
	counts := map[string]int{}
	for _, tm := range teams.Teams {
		_, body := seatReq(t, s, ceilTokPlain, "", "GET", "/v1/teams/"+tm.ID+"/evals", nil)
		counts[tm.Name] = strings.Count(body, `"outcome"`)
	}
	if counts["core"] != 1 || counts["core2"] != 1 {
		t.Fatalf("recorded evals = %v, want 1 each", counts)
	}
}

// After idle the leader's turn takes no team letter: POST /v1/mail answers 409 with a readable
// message; a member and a person are not bound.
func TestTeamIdleThenLetterIs409(t *testing.T) {
	s, _, _, _ := leaderWorld(t)
	send := func(token, sid, to string) (int, string) {
		return seatReq(t, s, token, sid, http.MethodPost, "/v1/mail", map[string]any{"to": to, "body": "work", "project": "p1"})
	}
	if status, resp := send(ceilTokPlain, "sess-lead2", "seat:b"); status != 200 {
		t.Fatalf("dispatch before idle = %d %s", status, resp)
	}
	evalCall(t, s, ceilTokPlain, "sess-lead2", map[string]any{"outcome": "idle", "reason": "nothing"}, 200)
	if status, resp := send(ceilTokPlain, "sess-lead2", "seat:b"); status != http.StatusConflict || !strings.Contains(resp, "idle") {
		t.Fatalf("dispatch after idle = %d %s, want 409 naming idle", status, resp)
	}
	evalCall(t, s, ceilTokPlain, "sess-lead2", map[string]any{"outcome": "dispatched"}, 409)
	if status, resp := send(ceilTokPlain, "", "team:core2"); status != 200 {
		t.Fatalf("a person's letter is not bound = %d %s", status, resp)
	}
}

// Terminal end: the UserPromptSubmit heartbeat of the leader session carries the block and opens
// its turn; the Stop heartbeat closes it, and a turn without an eval is recorded as missing. A
// member's session and every other event carry nothing.
func TestLeaderSessionHeartbeatCarriesTheBlock(t *testing.T) {
	s, _, _, _ := leaderWorld(t)
	beat := func(sid, event string) map[string]any {
		status, resp := seatReq(t, s, ceilTokPlain, "", http.MethodPost, "/v1/sessions/"+sid+"/heartbeat", map[string]any{"event": event})
		if status != 200 {
			t.Fatalf("heartbeat %s %s = %d %s", sid, event, status, resp)
		}
		var v map[string]any
		_ = json.Unmarshal([]byte(resp), &v)
		return v
	}
	v := beat("sess-lead2", "UserPromptSubmit")
	note, _ := v["team_note"].(string)
	if !strings.Contains(note, "core2") || !strings.Contains(note, "gofer team eval") || !strings.Contains(note, "- b（") {
		t.Fatalf("leader block = %q", note)
	}
	if v := beat("sess-b", "UserPromptSubmit"); v["team_note"] != nil {
		t.Fatalf("a member got a block: %v", v["team_note"])
	}
	if v := beat("sess-lead2", "Stop"); v["team_note"] != nil {
		t.Fatalf("Stop carries no block: %v", v["team_note"])
	}
	_, list := seatReq(t, s, ceilTokPlain, "", "GET", "/v1/teams?project=p1", nil)
	var teams struct {
		Teams []teamView `json:"teams"`
	}
	_ = json.Unmarshal([]byte(list), &teams)
	for _, tm := range teams.Teams {
		_, body := seatReq(t, s, ceilTokPlain, "", "GET", "/v1/teams/"+tm.ID+"/evals", nil)
		if tm.Name == "core2" && !strings.Contains(body, "missing_eval") {
			t.Fatalf("the turn that ended without an eval must be recorded: %s", body)
		}
		if tm.Name == "core" && strings.Contains(body, "missing_eval") {
			t.Fatalf("the other team's evals: %s", body)
		}
	}
	// with an eval in the turn nothing is missing
	beat("sess-lead2", "UserPromptSubmit")
	evalCall(t, s, ceilTokPlain, "sess-lead2", map[string]any{"outcome": "idle"}, 200)
	beat("sess-lead2", "Stop")
}

// The leader cannot finish a work item: `stage done` from the session sitting in the leader seat
// is refused when the next step is done; earlier steps pass, and a person's done is untouched.
func TestLeaderStageDoneRefusedIntoTheFinalStep(t *testing.T) {
	s := newTestServer(t, testToken, false)
	ids := map[string]string{}
	for _, n := range []string{"lead", "a"} {
		_, resp := seatReq(t, s, testToken, "", "POST", "/v1/seats", map[string]any{"project_key": "self", "name": n})
		var sv seatView
		if err := json.Unmarshal([]byte(resp), &sv); err != nil || sv.ID == "" {
			t.Fatalf("seat %s: %s", n, resp)
		}
		ids[n] = sv.ID
	}
	registerSeatSession(t, s, "sess-lead", "self", "")
	registerSeatSession(t, s, "sess-a", "self", "")
	for n, ref := range map[string]string{"lead": "sess-lead", "a": "sess-a"} {
		if _, err := s.jobs.Meta().BindSeat(ids[n], ref, false, "user"); err != nil {
			t.Fatal(err)
		}
	}
	if status, resp := seatReq(t, s, testToken, "", "POST", "/v1/teams", map[string]any{"project_key": "self", "name": "core", "leader": "lead", "members": []string{"a"}}); status != 200 {
		t.Fatalf("team: %d %s", status, resp)
	}
	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-9", "template": "small", "project": "self", "title": "fix"}, http.StatusCreated, &v)
	base := "/v1/work-items/" + v.WorkItemID + "/stage/done"
	done := func(sid string) (int, string) { return seatReq(t, s, testToken, sid, http.MethodPost, base, nil) }

	// doing -> verify: the next step is not done, the leader may move it (and so may a member)
	if status, resp := done("sess-lead"); status != 200 || !strings.Contains(resp, `"verify"`) {
		t.Fatalf("leader done at doing = %d %s", status, resp)
	}
	// verify -> done: refused for the leader, with a readable reason; the flow stays
	status, resp := done("sess-lead")
	if status != http.StatusForbidden || !strings.Contains(resp, "only a person completes") {
		t.Fatalf("leader done at verify = %d %s, want 403", status, resp)
	}
	var got stageViewBody
	stageCall(t, s, http.MethodGet, "/v1/work-items/"+v.WorkItemID+"/stage", nil, http.StatusOK, &got)
	if got.Step != "verify" || got.Status != "active" {
		t.Fatalf("flow after the refusal = %+v", got)
	}
	// a member and a person are not refused (the gate itself may not pass; that is not a 403)
	if status, resp := done("sess-a"); status != 200 {
		t.Fatalf("member done at verify = %d %s", status, resp)
	}
	if status, resp := done(""); status != 200 {
		t.Fatalf("person done at verify = %d %s", status, resp)
	}
}
