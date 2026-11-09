package httpapi

import (
	"net/http"
	"testing"
)

// TestOpenTurnNeedsReply goes through the real route: the flag in the request is
// stored on the turn and shown in the view; an absent flag reads false.
func TestOpenTurnNeedsReply(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "sid-nr", "agent": "claude", "event": "SessionStart",
	})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-nr/relay", testToken, map[string]any{"mode": "on"})
	resp.Body.Close()

	open := func(req map[string]any) decisionView {
		resp := do(t, s, http.MethodPost, "/v1/sessions/sid-nr/turns", testToken, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("open turn status=%d", resp.StatusCode)
		}
		var v decisionView
		decode(t, resp, &v)
		return v
	}
	v := open(map[string]any{"body": "pick one", "timeout_sec": 120, "needs_reply": true})
	if !v.NeedsReply {
		t.Fatalf("view needs_reply = false, want true")
	}
	d, ok, err := s.jobs.Meta().GetDecision(v.ID)
	if err != nil || !ok || !d.NeedsReply {
		t.Fatalf("stored decision = %+v ok=%v err=%v, want needs_reply", d, ok, err)
	}

	v = open(map[string]any{"body": "all done", "timeout_sec": 120})
	d, _, _ = s.jobs.Meta().GetDecision(v.ID)
	if v.NeedsReply || d.NeedsReply {
		t.Fatalf("absent flag stored as needs_reply: view=%v stored=%v", v.NeedsReply, d.NeedsReply)
	}
}
