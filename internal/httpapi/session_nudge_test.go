package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func nudgeBody(kind string, sec int64, text string) map[string]any {
	return map[string]any{"kind": kind, "interval_sec": sec, "text": text}
}

func TestSessionNudgeAPILifecycle(t *testing.T) {
	s := newTestServer(t, testToken, false)
	s.relay.SetMessenger(&y6Messenger{})
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "ng-api", "agent": "claude", "runner": "server", "project_key": "self", "event": "SessionStart",
		"peer_name": "inspect-22", "peer_messaging": true,
	})
	resp.Body.Close()

	// invalid input
	for _, b := range []map[string]any{nudgeBody("every", 5, "x"), nudgeBody("weekly", 600, "x"), nudgeBody("every", 600, " ")} {
		resp = do(t, s, http.MethodPost, "/v1/sessions/ng-api/nudges", testToken, b)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid nudge %v = %d, want 400", b, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp = do(t, s, http.MethodPost, "/v1/sessions/nope/nudges", testToken, nudgeBody("every", 600, "x"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodPost, "/v1/sessions/ng-api/nudges", testToken, nudgeBody("every", 600, "keep going"))
	var v sessionNudgeView
	decode(t, resp, &v)
	if v.ID == "" || v.State != "active" || v.Kind != "every" || v.IntervalSec != 600 || v.NextRunAt == 0 {
		t.Fatalf("created nudge = %+v", v)
	}

	var list struct {
		Nudges []sessionNudgeView `json:"nudges"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/sessions/ng-api/nudges", testToken, nil), &list)
	if len(list.Nudges) != 1 || list.Nudges[0].ID != v.ID {
		t.Fatalf("list = %+v", list)
	}
	decode(t, do(t, s, http.MethodGet, "/v1/nudges", testToken, nil), &list)
	if len(list.Nudges) != 1 {
		t.Fatalf("list all = %+v", list)
	}

	// the sweeper delivers through the same ladder as the web message box
	if n := s.relay.SweepNudges(context.Background(), time.Now().Unix()+601); n != 1 {
		t.Fatalf("sweep delivered %d, want 1", n)
	}
	decode(t, do(t, s, http.MethodGet, "/v1/sessions/ng-api/nudges", testToken, nil), &list)
	if list.Nudges[0].FireCount != 1 {
		t.Fatalf("after sweep = %+v", list.Nudges[0])
	}

	resp = do(t, s, http.MethodPatch, "/v1/nudges/"+v.ID, testToken, map[string]any{"state": "paused"})
	decode(t, resp, &v)
	if v.State != "paused" {
		t.Fatalf("paused nudge = %+v", v)
	}
	resp = do(t, s, http.MethodPatch, "/v1/nudges/"+v.ID, testToken, map[string]any{"state": "paused"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("double pause = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPatch, "/v1/nudges/"+v.ID, testToken, map[string]any{"state": "bogus"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus state = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	decode(t, do(t, s, http.MethodPatch, "/v1/nudges/"+v.ID, testToken, map[string]any{"state": "active"}), &v)
	if v.State != "active" {
		t.Fatalf("resumed nudge = %+v", v)
	}

	resp = do(t, s, http.MethodDelete, "/v1/nudges/"+v.ID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodDelete, "/v1/nudges/"+v.ID, testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSessionNudgePermissions(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}, {ID: "bob", Token: "tok-bob"}},
		Workers: map[string]config.WorkerAuthConfig{"worker-1": {Token: "tok-worker"}},
	})
	registerOwnedSession(t, s, "tok-alice", "ng-perm")
	resp := do(t, s, http.MethodPost, "/v1/sessions/ng-perm/nudges", "tok-alice", nudgeBody("every", 600, "x"))
	var v sessionNudgeView
	decode(t, resp, &v)
	if v.ID == "" {
		t.Fatal("owner could not set a nudge")
	}

	// A worker token, and a person who does not own the session, are refused.
	for _, tok := range []string{"tok-worker", "tok-bob"} {
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/v1/sessions/ng-perm/nudges"},
			{http.MethodPatch, "/v1/nudges/" + v.ID},
			{http.MethodDelete, "/v1/nudges/" + v.ID},
		} {
			resp = do(t, s, c.method, c.path, tok, map[string]any{"state": "paused", "kind": "every", "interval_sec": 600, "text": "x"})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s %s = %d, want 403", tok, c.method, c.path, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}

	// Job credentials — member, leader and steward — can never write; the steward cannot
	// even read them (its read allowlist does not include nudges).
	for name, kind := range map[string]string{"member": jobstore.JobCredentialMember, "leader": jobstore.JobCredentialLeader, "steward": jobstore.JobCredentialSteward} {
		tok := seedJobToken(t, s, "job-ng-"+name, kind, "")
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/v1/sessions/ng-perm/nudges"},
			{http.MethodPatch, "/v1/nudges/" + v.ID},
			{http.MethodDelete, "/v1/nudges/" + v.ID},
		} {
			resp = do(t, s, c.method, c.path, tok, map[string]any{"state": "paused", "kind": "every", "interval_sec": 600, "text": "x"})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s %s = %d, want 403", name, c.method, c.path, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}
	steward := seedJobToken(t, s, "job-ng-steward", jobstore.JobCredentialSteward, "")
	resp = do(t, s, http.MethodGet, "/v1/sessions/ng-perm/nudges", steward, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("steward GET nudges = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// untouched
	got, ok, _ := s.jobs.Meta().GetSessionNudge(v.ID)
	if !ok || got.State != jobstore.NudgeActive {
		t.Fatalf("nudge changed by a refused caller: %+v", got)
	}
}
