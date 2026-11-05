package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// The peer address is WRITE-ONLY (design 2026-11-05): the hook's register and heartbeat
// carry it, the server validates and stores it, and no session read serves it back —
// an address is a capability, not display data.
func TestSessionPeerSocketStoredNotServed(t *testing.T) {
	s := newTestServer(t, testToken, false)
	const sid = "peer-socket-http"
	const socket = "/run/user/1000/cc-socks/4242.sock"

	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": sid, "agent": "claude", "runner": "local", "event": "SessionStart",
		"peer_name": "proj-4d", "peer_messaging": true, "peer_socket": socket,
	})
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register = %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "peer_socket") || strings.Contains(body, "cc-socks") {
		t.Fatalf("the register response must not carry the address: %s", body)
	}
	a, ok, err := s.jobs.Meta().GetAgentSession(sid)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, socket, a.PeerSocket, "the registration stored the address")

	// A beat replaces it with the process's current value.
	const next = "/run/user/1000/cc-socks/5151.sock"
	resp = do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/heartbeat", testToken, map[string]any{
		"event": "Notification", "peer_socket": next,
	})
	body = bodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat = %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "cc-socks") {
		t.Fatalf("the heartbeat response must not carry the address: %s", body)
	}
	a, _, err = s.jobs.Meta().GetAgentSession(sid)
	assert.NoErr(t, err)
	assert.Eq(t, next, a.PeerSocket)

	// An address outside the whitelist is dropped, so the session falls back to the
	// name path instead of pointing at somebody else's socket.
	resp = do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/heartbeat", testToken, map[string]any{
		"event": "Notification", "peer_socket": "/var/run/docker.sock",
	})
	bodyString(t, resp)
	a, _, err = s.jobs.Meta().GetAgentSession(sid)
	assert.NoErr(t, err)
	assert.Eq(t, "", a.PeerSocket)

	// The single-session read has no such field either.
	resp = do(t, s, http.MethodGet, "/v1/sessions/"+sid, testToken, nil)
	if body = bodyString(t, resp); strings.Contains(body, "peer_socket") {
		t.Fatalf("GET /v1/sessions/{sid} must not carry peer_socket: %s", body)
	}
}
