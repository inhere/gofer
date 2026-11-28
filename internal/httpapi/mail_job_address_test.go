package httpapi

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// A job credential names its recipient directly: a session, a seat, a team or its caller; a
// search address stays refused (it would let the job probe sessions it does not know about).
func TestCheckJobMailAddress(t *testing.T) {
	t.Parallel()
	for _, to := range []string{"session:s1", "caller", "", "seat:review@proj", "team:core"} {
		if err := checkJobMailAddress(to); err != nil {
			t.Errorf("checkJobMailAddress(%q) = %v, want allowed", to, err)
		}
	}
	for _, to := range []string{"topic:login", "path:src", "agent:helper"} {
		if err := checkJobMailAddress(to); err == nil {
			t.Errorf("checkJobMailAddress(%q) allowed, want refused", to)
		}
	}
}

// A leader sitting in an ACP job dispatches by mailing its members' seats with its job
// credential, and a member job reports to team: — the recipient authorization must let a seat of
// the job's own project and caller through (found in the 3a trial: POST /v1/mail answered 403).
// Another project's seat stays refused.
func TestJobCredentialMailsSeatsOfItsProject(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	ids := map[string]string{}
	for _, n := range []string{"lead", "a"} {
		ids[n] = seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": n, "kind": "acp"}, 200).ID
	}
	leadTok := seedCredentialJob(t, s, "job-lead", "p1", "plain")
	memberTok := seedCredentialJob(t, s, "job-a", "p1", "plain")
	// The seated jobs are persistent sessions, so a letter to their seat reaches them (a one-off
	// job would hold the letter before the authorization is asked).
	for _, id := range []string{"job-lead", "job-a"} {
		rec, ok, err := s.jobs.Meta().GetJob(id)
		if err != nil || !ok {
			t.Fatalf("get %s: %v", id, err)
		}
		rec.RequestJSON = `{"session":true}`
		if err := s.jobs.Meta().UpsertJob(rec); err != nil {
			t.Fatal(err)
		}
	}
	for seat, ref := range map[string]string{"lead": "job-lead", "a": "job-a"} {
		if _, err := s.jobs.Meta().BindSeat(ids[seat], ref, false, "user"); err != nil {
			t.Fatal(err)
		}
	}
	teamJSON(t, s, ceilTokPlain, "POST", "/v1/teams", map[string]any{"project_key": "p1", "name": "core", "leader": "lead", "members": []string{"a"}}, 200)
	send := func(token, to string) (int, string) {
		return seatReq(t, s, token, "", "POST", "/v1/mail", map[string]any{"to": to, "body": "work", "project": "p1"})
	}
	if status, resp := send(leadTok, "seat:a"); status != 200 || strings.Contains(resp, `"held":true`) {
		t.Fatalf("leader job → seat:a = %d %s, want delivered", status, resp)
	}
	if status, resp := send(memberTok, "team:core"); status != 200 || strings.Contains(resp, `"held":true`) {
		t.Fatalf("member job → team:core = %d %s, want delivered", status, resp)
	}
	far, err := s.jobs.Meta().CreateSeat(jobstore.Seat{ProjectKey: "p2", Name: "far", CallerID: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	registerSeatSession(t, s, "sess-far", "p2", "plain")
	if _, err := s.jobs.Meta().BindSeat(far.ID, "sess-far", false, "user"); err != nil {
		t.Fatal(err)
	}
	if status, resp := seatReq(t, s, leadTok, "", "POST", "/v1/mail", map[string]any{"to": "seat:far@p2", "body": "x"}); status == 200 {
		t.Fatalf("leader job → another project's seat = %d %s, want refused", status, resp)
	}
}
