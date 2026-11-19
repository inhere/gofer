package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// seatNoticeSeat is the `seat` object of a session view, reduced to what these tests read.
type seatNoticeSeat struct {
	Name         string `json:"name"`
	Continuation string `json:"continuation"`
}

// seatNoticeOf reads a session view and returns the continuation text of its seat; it fails when
// the session sits in no seat. A bare registration answer carries the seat itself, a GET answer
// wraps the session in a `session` object, so both spellings are read here.
func seatNoticeOf(t *testing.T, s *Server, method, path string, body any) string {
	t.Helper()
	status, resp := seatReq(t, s, ceilTokPlain, "", method, path, body)
	if status != http.StatusOK {
		t.Fatalf("%s %s = %d: %s", method, path, status, resp)
	}
	var v struct {
		Seat    *seatNoticeSeat `json:"seat"`
		Session *struct {
			Seat *seatNoticeSeat `json:"seat"`
		} `json:"session"`
	}
	if err := json.Unmarshal([]byte(resp), &v); err != nil {
		t.Fatalf("decode %s %s: %v (%s)", method, path, err, resp)
	}
	seat := v.Seat
	if v.Session != nil {
		seat = v.Session.Seat
	}
	if seat == nil {
		t.Fatalf("%s %s: the session sits in no seat: %s", method, path, resp)
	}
	return seat.Continuation
}

// registerSessionPost is one hook SessionStart registration of sid in project p1.
func registerSessionPost(t *testing.T, s *Server, sid string) string {
	t.Helper()
	return seatNoticeOf(t, s, "POST", "/v1/sessions", map[string]any{
		"session_id": sid, "agent": "claude", "project_key": "p1", "event": "SessionStart",
	})
}

// TestSeatContinuationNoticeOnceOnRegistration: the session that has just taken a seat is told
// once where the earlier conversation stopped — the registration answer carries the text and
// marks it delivered in the same request, so the next registration does not. Reading the session
// view leaves the notice owed, and moving the seat to another session tells that one once more.
func TestSeatContinuationNoticeOnceOnRegistration(t *testing.T) {
	s := newCeilingServer(t)
	x := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{
		"project_key": "p1", "name": "dev", "purpose": "pr review", "charter": "be careful",
	}, 200)
	registerSeatSession(t, s, "sess-notice-a", "p1", "plain")
	registerSeatSession(t, s, "sess-notice-b", "p1", "plain")
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+x.ID+"/bind", map[string]any{"ref": "sess-notice-a"}, 200)

	// A read does not consume the notice: the first registration afterwards still carries it.
	if got := seatNoticeOf(t, s, "GET", "/v1/sessions/sess-notice-a", nil); got != "" {
		t.Fatalf("the session view handed out the continuation text: %q", got)
	}
	first := registerSessionPost(t, s, "sess-notice-a")
	if !strings.Contains(first, "dev") || !strings.Contains(first, "pr review") {
		t.Fatalf("first registration = %q, want the seat name and its purpose", first)
	}
	// The seat block the hook renders carries the charter; the notice that rides it must not paste
	// the same text again.
	if strings.Contains(first, "be careful") {
		t.Fatalf("first registration = %q, the charter is rendered by the seat block, not repeated by the notice", first)
	}

	// The second registration does not repeat it, and another read in between does not re-arm it.
	if got := seatNoticeOf(t, s, "GET", "/v1/sessions/sess-notice-a", nil); got != "" {
		t.Fatalf("the session view handed out the continuation text again: %q", got)
	}
	if got := registerSessionPost(t, s, "sess-notice-a"); got != "" {
		t.Fatalf("second registration = %q, want no continuation text", got)
	}

	// Moving the seat to another session owes that session its own notice, once.
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+x.ID+"/bind", map[string]any{"ref": "sess-notice-b", "replace": true}, 200)
	if got := seatNoticeOf(t, s, "GET", "/v1/sessions/sess-notice-b", nil); got != "" {
		t.Fatalf("the new session's view handed out the continuation text: %q", got)
	}
	if got := registerSessionPost(t, s, "sess-notice-b"); got == "" {
		t.Fatal("the session that took the seat was never told where the earlier conversation stopped")
	}
	if got := registerSessionPost(t, s, "sess-notice-b"); got != "" {
		t.Fatalf("the new session was told twice: %q", got)
	}

	// A session in no seat gets no seat object at all.
	status, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/sessions", map[string]any{
		"session_id": "sess-notice-free", "agent": "claude", "project_key": "p1",
	})
	if status != http.StatusOK || strings.Contains(resp, `"seat"`) {
		t.Fatalf("a session in no seat = %d %s", status, resp)
	}
}
