package httpapi

import (
	"net/http"
	"testing"
	"time"
)

// TestSessionRelayHTTPContract walks the session-relay endpoints (SESS-01 T3):
// register (project matched from cwd) → heartbeat (relay flag) → 409 when relay
// off → relay on → open turn → long-poll → answer via generic decision endpoint
// (bell path) → answered outcome + session back to running → say 409 → relay off
// expiry → 404s.
func TestSessionRelayHTTPContract(t *testing.T) {
	s := newTestServer(t, testToken, false)
	root := s.projects.Config().Projects["self"].HostPath

	// Missing session_id → 400.
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{"agent": "claude"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("register without session_id status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Register: project_key derived from cwd under the "self" project root.
	resp = do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-http", "agent": "claude", "cwd": root + "/sub", "event": "SessionStart",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status=%d, want 200", resp.StatusCode)
	}
	var sv sessionView
	decode(t, resp, &sv)
	if sv.ProjectKey != "self" || sv.State != "running" || sv.Relay {
		t.Fatalf("registered view mismatch: %+v", sv)
	}

	// Heartbeat Stop with relay off → idle, relay=false.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/heartbeat", testToken, map[string]any{
		"event": "Stop", "last_message": "what next?",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &sv)
	if sv.State != "idle" || sv.Relay || sv.LastMessage != "what next?" {
		t.Fatalf("heartbeat view mismatch: %+v", sv)
	}

	// Open turn while relay off → 409.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/turns", testToken, map[string]any{"body": "x"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("open turn relay-off status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// Relay on (web switch).
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/relay", testToken, map[string]any{"relay": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relay on status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &sv)
	if !sv.Relay {
		t.Fatalf("relay flag not set: %+v", sv)
	}

	// Open a turn → decision kind=relay, session waiting_reply.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/turns", testToken, map[string]any{
		"body": "need a decision", "timeout_sec": 120,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open turn status=%d, want 200", resp.StatusCode)
	}
	var turn decisionView
	decode(t, resp, &turn)
	if turn.Kind != "relay" || turn.SessionID != "sid-http" || turn.State != "OPEN" {
		t.Fatalf("turn view mismatch: %+v", turn)
	}

	// Detail: session waiting_reply + 1 turn; list sorts it first.
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-http", testToken, nil)
	var detail struct {
		Session sessionView    `json:"session"`
		Turns   []decisionView `json:"turns"`
	}
	decode(t, resp, &detail)
	if detail.Session.State != "waiting_reply" || len(detail.Turns) != 1 || detail.Session.TurnNo != 1 {
		t.Fatalf("detail mismatch: %+v turns=%d", detail.Session, len(detail.Turns))
	}
	resp = do(t, s, http.MethodGet, "/v1/sessions?project=self", testToken, nil)
	var list struct {
		Sessions []sessionView `json:"sessions"`
	}
	decode(t, resp, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "sid-http" {
		t.Fatalf("list mismatch: %+v", list.Sessions)
	}

	// Long-poll with a short wait while nobody answers → open.
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-http/turns/"+turn.ID+"?wait=1", testToken, nil)
	var st struct {
		Outcome  string       `json:"outcome"`
		Relay    bool         `json:"relay"`
		Decision decisionView `json:"decision"`
	}
	decode(t, resp, &st)
	if st.Outcome != "open" || !st.Relay {
		t.Fatalf("wait (unanswered) = %+v, want open", st)
	}

	// Answer from the bell (generic decision endpoint) → answered + running.
	go func() {
		time.Sleep(50 * time.Millisecond)
		r := do(t, s, http.MethodPost, "/v1/decisions/"+turn.ID+"/answer", testToken, map[string]string{"answer": "plan B"})
		r.Body.Close()
	}()
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-http/turns/"+turn.ID+"?wait=5", testToken, nil)
	decode(t, resp, &st)
	if st.Outcome != "answered" || st.Decision.Answer != "plan B" {
		t.Fatalf("wait (answered) = %+v", st)
	}
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-http", testToken, nil)
	decode(t, resp, &detail)
	if detail.Session.State != "running" {
		t.Fatalf("session after answer state=%s, want running", detail.Session.State)
	}

	// say with nothing open → 409; open another turn and say → 200.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/say", testToken, map[string]string{"answer": "hi"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("say without open turn status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/turns", testToken, map[string]any{"body": "again"})
	var turn2 decisionView
	decode(t, resp, &turn2)
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/say", testToken, map[string]string{"answer": "/off"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("say status=%d, want 200", resp.StatusCode)
	}
	var said decisionView
	decode(t, resp, &said)
	if said.ID != turn2.ID || said.Answer != "/off" {
		t.Fatalf("say answered wrong turn: %+v", said)
	}

	// Relay off while a turn is open → wait reports relay_off.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/turns", testToken, map[string]any{"body": "third"})
	var turn3 decisionView
	decode(t, resp, &turn3)
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/relay", testToken, map[string]any{"relay": false})
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-http/turns/"+turn3.ID, testToken, nil)
	decode(t, resp, &st)
	if st.Outcome != "relay_off" || st.Decision.State != "EXPIRED" {
		t.Fatalf("wait after relay off = %+v", st)
	}

	// UserPromptSubmit heartbeat → running (relay already off).
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-http/heartbeat", testToken, map[string]any{
		"event": "UserPromptSubmit", "title": "sub: first prompt",
	})
	decode(t, resp, &sv)
	if sv.State != "running" || sv.Relay || sv.Title != "sub: first prompt" {
		t.Fatalf("prompt heartbeat view mismatch: %+v", sv)
	}

	// Unknowns → 404; bad state → 400; delete → 200 then 404.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-nope/heartbeat", testToken, map[string]any{"event": "Stop"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("heartbeat unknown status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-http/turns/dec-nope", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wait unknown turn status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/sessions?state=weird", testToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("list bad state status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodDelete, "/v1/sessions/sid-http", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-http", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
