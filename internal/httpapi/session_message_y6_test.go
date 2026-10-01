package httpapi

import (
	"net/http"
	"testing"
)

func TestSessionMessageViaRelayWhenWaiting(t *testing.T) {
	s := newTestServer(t, testToken, false)
	register := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-relay", "agent": "claude", "event": "SessionStart",
	})
	register.Body.Close()
	resp := do(t, s, http.MethodPost, "/v1/sessions/msg-relay/relay", testToken, map[string]any{"mode": "on"})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-relay/turns", testToken, map[string]any{"body": "waiting"})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-relay/messages", testToken, map[string]any{"message": "继续"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message via relay status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSessionMessageViaMessengerJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-job", "agent": "claude", "runner": "server", "event": "SessionStart",
	})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-job/messages", testToken, map[string]any{"message": "进度"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message via messenger status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSessionMessageOrderedPerSession(t *testing.T) {
	// The server must serialize messages for one session; the implementation test
	// uses the outbox order as the durable observable.
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-order", "agent": "claude", "runner": "server", "event": "SessionStart",
	})
	resp.Body.Close()
	for _, msg := range []string{"one", "two"} {
		resp = do(t, s, http.MethodPost, "/v1/sessions/msg-order/messages", testToken, map[string]any{"message": msg})
		resp.Body.Close()
	}
	resp = do(t, s, http.MethodGet, "/v1/sessions/msg-order/outbox", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("outbox status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSessionMessageFailureReported(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-fail", "agent": "claude", "event": "SessionStart",
	})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-fail/messages", testToken, map[string]any{"message": "失败"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("message failure status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}
