package httpapi

import (
	"io"
	"net/http"
	"testing"
)

// gofer-m80j: a relay-on session's Stop wait is let go when its last sub-agent finishes,
// and no new turn may follow. The session then idles with nothing waiting; a web answer
// aimed at the turn that was just released must still reach it, and the view must say why
// the session is idle.
func TestSessionSayAfterSubagentRelease(t *testing.T) {
	s := newTestServer(t, testToken, false)
	s.relay.SetMessenger(&y6Messenger{})
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-subrel", "agent": "claude", "runner": "server", "project_key": "self", "event": "SessionStart",
		"peer_name": "inspect-23", "peer_status": "busy", "peer_messaging": true,
	})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-subrel/relay", testToken, map[string]any{"mode": "on"})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-subrel/turns", testToken, map[string]any{"body": "waiting"})
	resp.Body.Close()
	for _, ev := range []map[string]any{
		{"event": "SubagentStart", "subagent_id": "a1", "subagent_delta": 1},
		{"event": "SubagentStop", "subagent_id": "a1", "subagent_delta": -1},
	} {
		resp = do(t, s, http.MethodPost, "/v1/sessions/sid-subrel/heartbeat", testToken, ev)
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("heartbeat %v status=%d body=%s", ev["event"], resp.StatusCode, b)
		}
		resp.Body.Close()
	}

	var detail struct {
		Session sessionView `json:"session"`
	}
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-subrel", testToken, nil)
	decode(t, resp, &detail)
	if detail.Session.ReleasedBy != "subagent_done" || detail.Session.ReleasedAt == 0 {
		t.Fatalf("view after release: released_by=%q released_at=%d, want subagent_done", detail.Session.ReleasedBy, detail.Session.ReleasedAt)
	}

	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-subrel/say", testToken, map[string]string{"answer": "继续"})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("say after sub-agent release status=%d body=%s, want 200 (delivered through the message ladder)", resp.StatusCode, b)
	}
	resp.Body.Close()

	// The main agent's next Stop proves a new turn happened: the mark is gone and a say
	// with no open turn is refused again.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-subrel/heartbeat", testToken, map[string]any{"event": "Stop"})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("Stop heartbeat status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/sessions/sid-subrel", testToken, nil)
	detail.Session = sessionView{} // omitempty fields would keep the old values
	decode(t, resp, &detail)
	if detail.Session.ReleasedBy != "" {
		t.Fatalf("released_by after next Stop = %q, want cleared", detail.Session.ReleasedBy)
	}
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-subrel/say", testToken, map[string]string{"answer": "hi"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("say without open turn after the mark cleared status=%d, want 409", resp.StatusCode)
	}
}
