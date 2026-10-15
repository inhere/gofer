package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// gofer-eh53: after a token split (server.callers: the human's token vs the one
// the hooks use) a terminal keeps reporting for a session that belongs to the
// other caller. Refusing it is right; doing it silently is what these tests are
// about — the refusal is recorded on the session, the session view exposes it, and
// an admin can hand the session to the caller the hooks now use.

// newSplitTokenServer is the reported setup: the legacy server token (caller
// "default") carries can_admin, the agent / container hook token is "agent-dev"
// and carries nothing.
func newSplitTokenServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{
		Token: "tok-default",
		Callers: []config.CallerConfig{
			{ID: "default", CanAdmin: true},
			{ID: "agent-dev", Token: "tok-agent"},
			// A third caller with no capability at all: the "somebody else" of both
			// features (it may not take a session over, nor touch another plan).
			{ID: "other", Token: "tok-other"},
		},
		Workers:    map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
		Governance: config.GovernanceConfig{RequireAdminCapability: true},
	})
}

func registerSessionAs(t *testing.T, s *Server, token, sid string) sessionView {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions", token, map[string]any{
		"session_id": sid, "agent": "claude", "project_key": "self", "runner": "local",
		"cwd": ".", "event": "SessionStart",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register %s as %s: status=%d, want 200", sid, token, resp.StatusCode)
	}
	var v sessionView
	decode(t, resp, &v)
	return v
}

// beat posts a Stop heartbeat as token and returns the status plus the decoded view.
func beat(t *testing.T, s *Server, token, sid string) (int, sessionView) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/heartbeat", token,
		map[string]any{"event": "Stop", "last_message": "done"})
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return resp.StatusCode, sessionView{}
	}
	var v sessionView
	decode(t, resp, &v)
	return resp.StatusCode, v
}

// getSessionAs reads one session (any caller may read) and returns the view.
func getSessionAs(t *testing.T, s *Server, token, sid string) sessionView {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/sessions/"+sid, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session %s: status=%d, want 200", sid, resp.StatusCode)
	}
	var body struct {
		Session sessionView `json:"session"`
	}
	decode(t, resp, &body)
	return body.Session
}

// stirerrText reads an error response body as one string (for asserting the
// refusal actually names the operation).
func errText(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	decode(t, resp, &body)
	return body.Error + " " + body.Detail
}

// TestSessionRefusedReportIsRecordedAndCleared: a report from a caller that is not
// the owner is still refused, but the session now says so; the owner's own report
// clears the note. Covers the three refusing paths (register / heartbeat / reply).
func TestSessionRefusedReportIsRecordedAndCleared(t *testing.T) {
	t.Parallel()
	s := newSplitTokenServer(t)

	v := registerSessionAs(t, s, "tok-default", "sid-refused")
	if v.CallerID != "default" || v.RefusedCallerID != "" {
		t.Fatalf("fresh session: caller=%q refused=%q, want default / none", v.CallerID, v.RefusedCallerID)
	}

	// The hook switched to the agent token: the heartbeat is refused AND recorded.
	if status, _ := beat(t, s, "tok-agent", "sid-refused"); status != http.StatusForbidden {
		t.Fatalf("foreign heartbeat status=%d, want 403", status)
	}
	got := getSessionAs(t, s, "tok-default", "sid-refused")
	if got.RefusedCallerID != "agent-dev" || got.RefusedAt <= 0 {
		t.Fatalf("after refusal: refused_caller_id=%q refused_at=%d, want agent-dev / >0", got.RefusedCallerID, got.RefusedAt)
	}
	if got.CallerID != "default" {
		t.Fatalf("refusal must not move ownership: caller_id=%q", got.CallerID)
	}
	// The list carries it too (that is what the Sessions page reads).
	resp := do(t, s, http.MethodGet, "/v1/sessions", "tok-default", nil)
	var list struct {
		Sessions []sessionView `json:"sessions"`
	}
	decode(t, resp, &list)
	found := false
	for _, it := range list.Sessions {
		if it.SessionID == "sid-refused" {
			found = true
			if it.RefusedCallerID != "agent-dev" {
				t.Fatalf("list refused_caller_id=%q, want agent-dev", it.RefusedCallerID)
			}
		}
	}
	if !found {
		t.Fatal("sid-refused missing from the list")
	}

	// A re-registration under the wrong identity is refused and recorded just the same.
	resp = do(t, s, http.MethodPost, "/v1/sessions", "tok-agent", map[string]any{
		"session_id": "sid-refused", "agent": "claude", "project_key": "self", "runner": "local",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign re-register status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// So is a reply reported under the wrong identity.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-refused/replies", "tok-agent", map[string]any{"text": "hello"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign reply status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	if got = getSessionAs(t, s, "tok-default", "sid-refused"); got.RefusedCallerID != "agent-dev" {
		t.Fatalf("reply refusal not recorded: refused_caller_id=%q", got.RefusedCallerID)
	}

	// The owner reports again: 200, and the note is gone from the row itself.
	if status, v := beat(t, s, "tok-default", "sid-refused"); status != http.StatusOK {
		t.Fatalf("owner heartbeat status=%d, want 200", status)
	} else if v.RefusedCallerID != "" || v.RefusedAt != 0 {
		t.Fatalf("owner beat did not clear the note: refused=%q/%d", v.RefusedCallerID, v.RefusedAt)
	}
	a, ok, err := s.jobs.Meta().GetAgentSession("sid-refused")
	if err != nil || !ok {
		t.Fatalf("get stored session: ok=%v err=%v", ok, err)
	}
	if a.RefusedCallerID != "" || a.RefusedAt != 0 {
		t.Fatalf("stored refusal not cleared: %q/%d", a.RefusedCallerID, a.RefusedAt)
	}
}

// TestSessionOwnerTransferPermissions: who may hand a session over (can_admin
// only), what the request must contain, and which answers the failure cases get.
func TestSessionOwnerTransferPermissions(t *testing.T) {
	t.Parallel()
	s := newSplitTokenServer(t)
	registerSessionAs(t, s, "tok-default", "sid-perm")
	if status, _ := beat(t, s, "tok-agent", "sid-perm"); status != http.StatusForbidden {
		t.Fatalf("setup: foreign heartbeat status=%d, want 403", status)
	}
	jobTok := seedJobToken(t, s, "job-sess", jobstore.JobCredentialMember, "")

	cases := []struct {
		name, token, path string
		body              map[string]any
		want              int
		wantIn            string
	}{
		{"non-admin caller", "tok-agent", "/v1/sessions/sid-perm/owner", map[string]any{"caller_id": "agent-dev"},
			http.StatusForbidden, "session.transfer"},
		{"worker credential", "tok-worker", "/v1/sessions/sid-perm/owner", map[string]any{"caller_id": "agent-dev"},
			http.StatusForbidden, "worker"},
		{"job credential", jobTok, "/v1/sessions/sid-perm/owner", map[string]any{"caller_id": "agent-dev"},
			http.StatusForbidden, "may not hand a session"},
		{"unknown caller", "tok-default", "/v1/sessions/sid-perm/owner", map[string]any{"caller_id": "nobody"},
			http.StatusBadRequest, "unknown caller"},
		{"worker id is not a session owner", "tok-default", "/v1/sessions/sid-perm/owner", map[string]any{"caller_id": "w1"},
			http.StatusBadRequest, "unknown caller"},
		{"missing caller_id", "tok-default", "/v1/sessions/sid-perm/owner", map[string]any{},
			http.StatusBadRequest, "caller_id required"},
		{"unknown session", "tok-default", "/v1/sessions/sid-none/owner", map[string]any{"caller_id": "agent-dev"},
			http.StatusNotFound, "unknown session"},
	}
	for _, tc := range cases {
		resp := do(t, s, http.MethodPost, tc.path, tc.token, tc.body)
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: status=%d, want %d (%s)", tc.name, resp.StatusCode, tc.want, errText(t, resp))
		}
		if text := errText(t, resp); !strings.Contains(text, tc.wantIn) {
			t.Fatalf("%s: message %q does not mention %q", tc.name, text, tc.wantIn)
		}
	}

	// None of the refusals moved the owner.
	if got := getSessionAs(t, s, "tok-default", "sid-perm"); got.CallerID != "default" {
		t.Fatalf("refused transfer changed the owner: %q", got.CallerID)
	}
}

// TestSessionOwnerTransferHandsTheSessionOver: the admin path end to end — after
// the hand-over the caller the hooks use reports successfully, and the old owner
// is now the refused one (recorded, so the web can explain it).
func TestSessionOwnerTransferHandsTheSessionOver(t *testing.T) {
	t.Parallel()
	s := newSplitTokenServer(t)
	registerSessionAs(t, s, "tok-default", "sid-trans")
	if status, _ := beat(t, s, "tok-agent", "sid-trans"); status != http.StatusForbidden {
		t.Fatalf("setup: foreign heartbeat status=%d, want 403", status)
	}

	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-trans/owner", "tok-default", map[string]any{"caller_id": "agent-dev"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin transfer status=%d, want 200 (%s)", resp.StatusCode, errText(t, resp))
	}
	var v sessionView
	decode(t, resp, &v)
	if v.CallerID != "agent-dev" {
		t.Fatalf("owner after transfer=%q, want agent-dev", v.CallerID)
	}
	if v.RefusedCallerID != "" || v.RefusedAt != 0 {
		t.Fatalf("transfer must clear the refusal note: %q/%d", v.RefusedCallerID, v.RefusedAt)
	}
	if v.SessionID != "sid-trans" || v.State == "" {
		t.Fatalf("transfer must return the whole session: %+v", v)
	}

	// The agent token's beats land again — that is the point of the hand-over.
	if status, v := beat(t, s, "tok-agent", "sid-trans"); status != http.StatusOK {
		t.Fatalf("new owner heartbeat status=%d, want 200", status)
	} else if v.CallerID != "agent-dev" {
		t.Fatalf("new owner beat caller=%q", v.CallerID)
	}
	// ...and the previous owner is the one refused now, recorded for the web.
	if status, _ := beat(t, s, "tok-default", "sid-trans"); status != http.StatusForbidden {
		t.Fatalf("previous owner heartbeat status=%d, want 403", status)
	}
	if got := getSessionAs(t, s, "tok-default", "sid-trans"); got.RefusedCallerID != "default" {
		t.Fatalf("previous owner refusal not recorded: %q", got.RefusedCallerID)
	}
	a, ok, err := s.jobs.Meta().GetAgentSession("sid-trans")
	if err != nil || !ok {
		t.Fatalf("get stored session: ok=%v err=%v", ok, err)
	}
	if a.CallerID != "agent-dev" || a.RefusedCallerID != "default" {
		t.Fatalf("stored row after transfer: caller=%q refused=%q", a.CallerID, a.RefusedCallerID)
	}
}
