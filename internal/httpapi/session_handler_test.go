package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
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

// sessionAutoArmServer builds a test server whose idle auto-arm threshold is
// `sec` (0 = disabled), mirroring the shared "self" project wiring.
func sessionAutoArmServer(t *testing.T, sec int) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{
		Token: testToken, SessionAutoRelayIdleSec: &sec,
	})
}

func registerIdleSession(t *testing.T, s *Server, sid string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": sid, "agent": "claude", "cwd": ".", "event": "SessionStart",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register %s status=%d, want 200", sid, resp.StatusCode)
	}
	resp.Body.Close()
}

// heartbeatStop posts a Stop heartbeat carrying an idle reading and returns the
// resulting view.
func heartbeatStop(t *testing.T, s *Server, sid string, idleSec int64) sessionView {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/heartbeat", testToken, map[string]any{
		"event": "Stop", "last_message": "what next?", "idle_sec": idleSec,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status=%d, want 200", resp.StatusCode)
	}
	var sv sessionView
	decode(t, resp, &sv)
	return sv
}

// TestSessionHeartbeatAutoArmsOnIdle walks the SR-A5 idle auto-arm contract: the
// reported idle reading arms relay (threshold 300s) without anyone touching the
// switch, an auto-armed wait can be opened and is released only when the hook
// sees the human back, and an explicitly switched-on relay is never released
// that way.
func TestSessionHeartbeatAutoArmsOnIdle(t *testing.T) {
	s := sessionAutoArmServer(t, 300)
	registerIdleSession(t, s, "sid-idle")

	// Away for 10 min → armed (relay itself stays off: the switch is the human's).
	sv := heartbeatStop(t, s, "sid-idle", 600)
	if !sv.AutoArmed || sv.Relay || sv.IdleSec != 600 {
		t.Fatalf("idle 600 view=%+v, want auto_armed with relay off", sv)
	}
	// Just typed (10s) → not armed; unknown (-1) → never armed.
	if sv = heartbeatStop(t, s, "sid-idle", 10); sv.AutoArmed {
		t.Fatalf("idle 10 view=%+v, want not armed", sv)
	}
	if sv = heartbeatStop(t, s, "sid-idle", -1); sv.AutoArmed || sv.IdleSec != -1 {
		t.Fatalf("idle -1 view=%+v, want unarmed unknown", sv)
	}
	// A heartbeat without a reading keeps the last one instead of clearing it.
	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-idle/heartbeat", testToken, map[string]any{
		"event": "Notification",
	})
	decode(t, resp, &sv)
	if sv.IdleSec != -1 {
		t.Fatalf("beat without reading changed idle to %d", sv.IdleSec)
	}

	// Armed again → the Stop hook may open a turn even though relay is off.
	heartbeatStop(t, s, "sid-idle", 600)
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-idle/turns", testToken, map[string]any{"body": "A or B?", "timeout_sec": 120})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open turn on auto-armed session status=%d, want 200", resp.StatusCode)
	}
	var turn decisionView
	decode(t, resp, &turn)
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-idle", testToken, nil)
	var detail struct {
		Session sessionView `json:"session"`
	}
	decode(t, resp, &detail)
	if detail.Session.State != "waiting_reply" {
		t.Fatalf("auto-armed wait state=%s, want waiting_reply", detail.Session.State)
	}
	// Still away → the release request keeps the wait alive.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-idle/turns/"+turn.ID+"/release", testToken, map[string]any{"idle_sec": 420})
	var rel struct {
		Released bool `json:"released"`
	}
	decode(t, resp, &rel)
	if rel.Released {
		t.Fatalf("release while still away must not fire")
	}
	// Human back at the keyboard → released, turn EXPIRED + tagged, session idle.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-idle/turns/"+turn.ID+"/release", testToken, map[string]any{"idle_sec": 3})
	decode(t, resp, &rel)
	if !rel.Released {
		t.Fatalf("release after user return must fire")
	}
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-idle", testToken, nil)
	var after struct {
		Session sessionView    `json:"session"`
		Turns   []decisionView `json:"turns"`
	}
	decode(t, resp, &after)
	if after.Session.State != "idle" {
		t.Fatalf("state after release=%s, want idle", after.Session.State)
	}
	if len(after.Turns) != 1 || after.Turns[0].State != "EXPIRED" || after.Turns[0].ReleasedBy != "user_returned" {
		t.Fatalf("turn after release=%+v, want EXPIRED released_by=user_returned", after.Turns)
	}

	// An EXPLICIT switch is not released by the idle reading (design SR-A5 §3):
	// the human owns that switch, only they (or typing) turn it off.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-idle/relay", testToken, map[string]any{"relay": true})
	resp.Body.Close()
	heartbeatStop(t, s, "sid-idle", 600)
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-idle/turns", testToken, map[string]any{"body": "still here?", "timeout_sec": 120})
	var explicit decisionView
	decode(t, resp, &explicit)
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-idle/turns/"+explicit.ID+"/release", testToken, map[string]any{"idle_sec": 1})
	decode(t, resp, &rel)
	if rel.Released {
		t.Fatalf("explicit relay must not be released by the idle probe")
	}
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-idle/say", testToken, map[string]string{"answer": "/off"})
	resp.Body.Close()
}

// TestSessionAutoRelayDisabledByZero pins `session_auto_relay_idle_sec: 0`: no
// idle reading ever arms relay, so a Stop with relay off stays a plain stop.
func TestSessionAutoRelayDisabledByZero(t *testing.T) {
	s := sessionAutoArmServer(t, 0)
	registerIdleSession(t, s, "sid-noidle")

	if sv := heartbeatStop(t, s, "sid-noidle", 9999); sv.AutoArmed {
		t.Fatalf("auto-arm disabled but view=%+v", sv)
	}
	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-noidle/turns", testToken, map[string]any{"body": "x"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("open turn with auto-arm disabled status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-noidle/turns/dec-nope/release", testToken, map[string]any{"idle_sec": 0})
	var rel struct {
		Released bool `json:"released"`
	}
	decode(t, resp, &rel)
	if rel.Released {
		t.Fatalf("release must be a no-op with auto-arm disabled")
	}

	// Unset threshold on a plain server falls back to the 5-minute default.
	def := newTestServer(t, testToken, false)
	registerIdleSession(t, def, "sid-def")
	if sv := heartbeatStop(t, def, "sid-def", 600); !sv.AutoArmed {
		t.Fatalf("default threshold should arm at idle 600: %+v", sv)
	}
	if sv := heartbeatStop(t, def, "sid-def", 299); sv.AutoArmed {
		t.Fatalf("idle 299 below the 5-minute default must not arm: %+v", sv)
	}
}
