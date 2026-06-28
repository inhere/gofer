package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/presence"
)

// newPresenceServer builds the standard test server and wires an E36 presence
// service over a fresh store, so the /v1/agents/* + /v1/messages routes mount.
func newPresenceServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, testToken, false)
	store := openTestStore(t, t.TempDir())
	s.SetPresence(presence.NewService(store))
	return s
}

func TestPresenceRoutesAbsentWithoutService(t *testing.T) {
	// A server without SetPresence must NOT mount the presence routes. A POST is a
	// clean signal: an unmatched GET would be swallowed by the web SPA NotFound
	// fallback, but an unmatched POST returns a bare 404.
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/messages", testToken, postMessageReq{To: "x", Kind: "task"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("presence route should be absent (404), got %d", resp.StatusCode)
	}
}

func TestRegisterListAndPoll(t *testing.T) {
	s := newPresenceServer(t)

	// Register alice + bob.
	a := registerAgent(t, s, "alice", "reviewer")
	b := registerAgent(t, s, "bob", "")
	if a.AgentID == "" || a.AgentToken == "" {
		t.Fatalf("register alice returned empty ids: %+v", a)
	}

	// Both appear in presence (no token leaked).
	listResp := do(t, s, http.MethodGet, "/v1/agents/presence", testToken, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("presence status=%d, want 200", listResp.StatusCode)
	}
	var list struct {
		Agents []presence.Agent `json:"agents"`
	}
	decode(t, listResp, &list)
	if len(list.Agents) != 2 {
		t.Fatalf("presence count=%d, want 2: %+v", len(list.Agents), list.Agents)
	}

	// alice → bob direct message; delivered=1.
	if n := postMessage(t, s, a.AgentID, b.AgentID, "task", "审 PR", "job:1"); n != 1 {
		t.Fatalf("delivered=%d, want 1", n)
	}

	// bob polls and consumes it.
	msgs := pollInbox(t, s, b.AgentID, b.AgentToken, http.StatusOK)
	if len(msgs) != 1 {
		t.Fatalf("bob inbox=%d, want 1", len(msgs))
	}
	if msgs[0].FromAgent != a.AgentID || msgs[0].Kind != "task" || msgs[0].Body != "审 PR" {
		t.Fatalf("unexpected message: %+v", msgs[0])
	}

	// Second poll is empty (already read).
	again := pollInbox(t, s, b.AgentID, b.AgentToken, http.StatusOK)
	if len(again) != 0 {
		t.Fatalf("bob inbox after read=%d, want 0", len(again))
	}
}

func TestPollWrongTokenForbidden(t *testing.T) {
	s := newPresenceServer(t)
	a := registerAgent(t, s, "alice", "")

	resp := do(t, s, http.MethodPost, "/v1/agents/"+a.AgentID+"/inbox/poll", testToken, pollInboxReq{AgentToken: "wrong"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("poll wrong token status=%d, want 403", resp.StatusCode)
	}
}

func TestPollUnknownAgentNotFound(t *testing.T) {
	s := newPresenceServer(t)
	resp := do(t, s, http.MethodPost, "/v1/agents/ghost/inbox/poll", testToken, pollInboxReq{AgentToken: "x"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("poll unknown agent status=%d, want 404", resp.StatusCode)
	}
}

func TestPostRoleFanOut(t *testing.T) {
	s := newPresenceServer(t)
	sender := registerAgent(t, s, "sender", "")
	registerAgent(t, s, "rev1", "reviewer")
	registerAgent(t, s, "rev2", "reviewer")

	// role: filter on the presence list works.
	listResp := do(t, s, http.MethodGet, "/v1/agents/presence?role=reviewer", testToken, nil)
	var list struct {
		Agents []presence.Agent `json:"agents"`
	}
	decode(t, listResp, &list)
	if len(list.Agents) != 2 {
		t.Fatalf("role=reviewer count=%d, want 2", len(list.Agents))
	}

	if n := postMessage(t, s, sender.AgentID, "role:reviewer", "task", "审 PR", ""); n != 2 {
		t.Fatalf("role fan-out delivered=%d, want 2", n)
	}
}

func TestDeregisterEndpoint(t *testing.T) {
	s := newPresenceServer(t)
	a := registerAgent(t, s, "alice", "")

	// Wrong token → 403.
	resp := do(t, s, http.MethodPost, "/v1/agents/"+a.AgentID+"/deregister", testToken, deregisterReq{AgentToken: "wrong"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("deregister wrong token status=%d, want 403", resp.StatusCode)
	}

	// Correct token → ok, and the agent disappears from presence.
	ok := do(t, s, http.MethodPost, "/v1/agents/"+a.AgentID+"/deregister", testToken, deregisterReq{AgentToken: a.AgentToken})
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("deregister status=%d, want 200", ok.StatusCode)
	}
	listResp := do(t, s, http.MethodGet, "/v1/agents/presence", testToken, nil)
	var list struct {
		Agents []presence.Agent `json:"agents"`
	}
	decode(t, listResp, &list)
	if len(list.Agents) != 0 {
		t.Fatalf("after deregister count=%d, want 0", len(list.Agents))
	}
}

// --- helpers ---

func registerAgent(t *testing.T, s *Server, name, role string) presence.RegisterResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/agents/register", testToken, registerAgentReq{Name: name, Role: role})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register %s status=%d, want 200", name, resp.StatusCode)
	}
	var out presence.RegisterResult
	decode(t, resp, &out)
	return out
}

func postMessage(t *testing.T, s *Server, from, to, kind, body, ref string) int {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/messages", testToken, postMessageReq{
		FromAgent: from, To: to, Kind: kind, Body: body, Ref: ref,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Delivered int `json:"delivered"`
	}
	decode(t, resp, &out)
	return out.Delivered
}

func pollInbox(t *testing.T, s *Server, id, token string, wantStatus int) []presence.Message {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/agents/"+id+"/inbox/poll", testToken, pollInboxReq{AgentToken: token})
	if resp.StatusCode != wantStatus {
		t.Fatalf("poll status=%d, want %d", resp.StatusCode, wantStatus)
	}
	var out struct {
		Messages []presence.Message `json:"messages"`
	}
	decode(t, resp, &out)
	return out.Messages
}
