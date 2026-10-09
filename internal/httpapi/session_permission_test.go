package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

func openPermissionHTTP(t *testing.T, s *Server, token, sid string) decisionView {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/permissions", token, map[string]any{
		"tool_name": "Bash", "summary": "Bash `rm -rf node_modules`", "input": `{"command":"rm -rf node_modules"}`,
		"suggestions": []map[string]string{{"label": "规则 Bash(rm:*)"}}, "fp": "fp-1", "timeout_sec": 600,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("open permission status=%d body=%s", resp.StatusCode, b)
	}
	var v decisionView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func statusOf(t *testing.T, s *Server, method, path, token string, body any) int {
	t.Helper()
	resp := do(t, s, method, path, token, body)
	resp.Body.Close()
	return resp.StatusCode
}

// TestSessionPermissionHTTP: the hook opens a prompt on a waiting session, only the
// session's owner answers it (not another person, not a worker, not a job / steward
// credential, not through the generic decision endpoint), the answer is audited and
// the hook's long poll sees it.
func TestSessionPermissionHTTP(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}, {ID: "bob", Token: "tok-bob"},
			{ID: "helper", Token: "tok-helper", CanAnswer: true}},
		Governance: config.GovernanceConfig{RequireAnswerCapability: true},
		Workers:    map[string]config.WorkerAuthConfig{"worker-1": {Token: "tok-worker"}},
	})
	sid := "sid-perm"
	for _, step := range []struct {
		path string
		body any
	}{
		{"/v1/sessions", map[string]any{"session_id": sid, "agent": "claude"}},
		{"/v1/sessions/" + sid + "/relay", map[string]any{"mode": "on"}},
	} {
		if code := statusOf(t, s, http.MethodPost, step.path, "tok-alice", step.body); code != http.StatusOK {
			t.Fatalf("POST %s status=%d", step.path, code)
		}
	}
	// Only the owner's hook may open one.
	if code := statusOf(t, s, http.MethodPost, "/v1/sessions/"+sid+"/permissions", "tok-bob",
		map[string]any{"tool_name": "Bash", "summary": "x"}); code != http.StatusForbidden {
		t.Fatalf("foreign open status=%d, want 403", code)
	}
	v := openPermissionHTTP(t, s, "tok-alice", sid)
	if v.Kind != jobstore.DecisionKindPermission || v.Permission == nil || v.Permission.ToolName != "Bash" ||
		len(v.Permission.Suggestions) != 1 {
		t.Fatalf("decision view = %+v", v)
	}

	answer := "/v1/sessions/" + sid + "/permissions/" + v.ID + "/answer"
	stewardTok := seedJobToken(t, s, "job-steward", jobstore.JobCredentialSteward, "")
	memberTok := seedJobToken(t, s, "job-member", jobstore.JobCredentialMember, "")
	for name, tok := range map[string]string{"bob": "tok-bob", "can_answer helper": "tok-helper", "worker": "tok-worker",
		"steward": stewardTok, "member job": memberTok} {
		if code := statusOf(t, s, http.MethodPost, answer, tok, map[string]string{"answer": "allow"}); code != http.StatusForbidden {
			t.Fatalf("%s answer status=%d, want 403", name, code)
		}
	}
	// The generic decision endpoint refuses it (structured answer, owner only).
	if code := statusOf(t, s, http.MethodPost, "/v1/decisions/"+v.ID+"/answer", "tok-alice",
		map[string]string{"answer": "allow"}); code != http.StatusConflict {
		t.Fatalf("generic answer status=%d, want 409", code)
	}
	// Free text into the reply box never answers it.
	if code := statusOf(t, s, http.MethodPost, "/v1/sessions/"+sid+"/say", "tok-alice",
		map[string]string{"answer": "yes"}); code != http.StatusConflict {
		t.Fatalf("say status=%d, want 409 (no open turn)", code)
	}
	if code := statusOf(t, s, http.MethodPost, answer, "tok-alice", map[string]string{"answer": "always:3"}); code != http.StatusBadRequest {
		t.Fatalf("bad suggestion status=%d, want 400", code)
	}
	resp := do(t, s, http.MethodPost, answer, "tok-alice", map[string]string{"answer": "always:0"})
	var got decisionView
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || got.State != jobstore.DecisionAnswered || got.Answer != "always:0" || got.AnsweredBy != "alice" {
		t.Fatalf("owner answer status=%d view=%+v", resp.StatusCode, got)
	}
	if ev, ok, err := s.jobs.Meta().LatestAuditEvent(sessionrelay.AuditPermissionAnswered, sid); err != nil || !ok || ev.Actor != "alice" {
		t.Fatalf("audit = %+v ok=%v err=%v", ev, ok, err)
	}
	resp = do(t, s, http.MethodGet, "/v1/sessions/"+sid+"/turns/"+v.ID, "tok-alice", nil)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"outcome":"answered"`) {
		t.Fatalf("wait turn = %s", b)
	}
	if code := statusOf(t, s, http.MethodPost, answer, "tok-alice", map[string]string{"answer": "deny"}); code != http.StatusConflict {
		t.Fatalf("second answer status=%d, want 409", code)
	}

	// An owner-less session (pre-owner row): any person may answer, a worker never.
	if _, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{SessionID: "sid-legacy", Agent: "claude", RelayMode: jobstore.RelayModeOn}); err != nil {
		t.Fatal(err)
	}
	lv := openPermissionHTTP(t, s, "tok-bob", "sid-legacy")
	legacy := "/v1/sessions/sid-legacy/permissions/" + lv.ID + "/answer"
	if code := statusOf(t, s, http.MethodPost, legacy, "tok-worker", map[string]string{"answer": "allow"}); code != http.StatusForbidden {
		t.Fatalf("worker on owner-less session status=%d, want 403", code)
	}
	if code := statusOf(t, s, http.MethodPost, legacy, "tok-bob", map[string]string{"answer": "allow"}); code != http.StatusOK {
		t.Fatalf("person on owner-less session status=%d, want 200", code)
	}
}

// TestSessionPermissionResolveAndRelayOff: a prompt on a session that is not
// waiting is refused (409, the hook leaves it to the terminal), and the PostToolUse
// resolve closes exactly the matching prompt.
func TestSessionPermissionResolveAndRelayOff(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}}})
	sid := "sid-res"
	if code := statusOf(t, s, http.MethodPost, "/v1/sessions", "tok-alice", map[string]any{"session_id": sid, "agent": "claude"}); code != http.StatusOK {
		t.Fatalf("register status=%d", code)
	}
	if code := statusOf(t, s, http.MethodPost, "/v1/sessions/"+sid+"/permissions", "tok-alice",
		map[string]any{"tool_name": "Bash", "summary": "x"}); code != http.StatusConflict {
		t.Fatalf("not-waiting open status=%d, want 409", code)
	}
	if code := statusOf(t, s, http.MethodPost, "/v1/sessions/"+sid+"/relay", "tok-alice", map[string]any{"mode": "on"}); code != http.StatusOK {
		t.Fatalf("relay on status=%d", code)
	}
	v := openPermissionHTTP(t, s, "tok-alice", sid)
	resp := do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/permissions/resolve", "tok-alice", map[string]string{"fp": "other"})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"resolved":0`) {
		t.Fatalf("resolve other = %s", b)
	}
	resp = do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/permissions/resolve", "tok-alice", map[string]string{"fp": "fp-1"})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"resolved":1`) {
		t.Fatalf("resolve fp-1 = %s", b)
	}
	d, _, _ := s.jobs.Meta().GetDecision(v.ID)
	if d.State != jobstore.DecisionExpired || d.ReleasedBy != sessionrelay.ReleaseByTerminal {
		t.Fatalf("decision after resolve = %+v", d)
	}
}
