package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
)

// The `effective` flag of GET /v1/auth/grants (gofer-x632) must agree with enforcement
// (sessionGrantHeld) at every step of a session's life.
func TestSessionGrantEffectiveFollowsEnforcement(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	st := s.jobs.Meta()
	const perm = authz.PermSessionReplyOthers
	check := func(step, id, sid string, want bool) {
		t.Helper()
		g := authGrantByID(t, s, id)
		if g.Effective != want {
			t.Errorf("%s: effective=%v, want %v (state=%s)", step, g.Effective, want, g.State)
		}
		if held := sessionGrantHeld(t, s, "tok-alice", sid, perm); held != g.Effective && g.State == grantStateActive {
			t.Errorf("%s: effective=%v disagrees with enforcement held=%v", step, g.Effective, held)
		}
	}

	registerSessionAs(t, s, "tok-alice", "sess-eff")
	g := createSessionGrant(t, s, "sess-eff", perm, 0)
	check("before end", g.ID, "sess-eff", true)

	if _, err := st.SetSessionState("sess-eff", jobstore.SessionEnded); err != nil {
		t.Fatal(err)
	}
	check("after end", g.ID, "sess-eff", false)

	if _, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-eff", Agent: "claude", CallerID: "alice"}); err != nil {
		t.Fatal(err)
	}
	check("revived (incarnation rotated)", g.ID, "sess-eff", false)
	if got := authGrantByID(t, s, g.ID); got.State != grantStateActive {
		t.Errorf("the row itself stays active, got %s", got.State)
	}
}

func TestSessionGrantEffectiveFalseWhenExpiredOrRevoked(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	st := s.jobs.Meta()
	const perm = authz.PermSessionReplyOthers

	registerSessionAs(t, s, "tok-alice", "sess-eff2")
	live := createSessionGrant(t, s, "sess-eff2", perm, 0)
	anchor, ok, err := st.SessionGrantAnchor("sess-eff2")
	if err != nil || !ok {
		t.Fatalf("anchor: ok=%v err=%v", ok, err)
	}
	expired, err := st.CreateAuthGrant(jobstore.AuthGrant{
		Subject: "session:sess-eff2", Pattern: perm, GrantedBy: "root",
		SessionOwner: anchor.Owner, SessionIncarnation: anchor.Incarnation,
		ExpiresAt: time.Now().Unix() - 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g := authGrantByID(t, s, expired.ID); g.Effective || g.State != grantStateExpired {
		t.Errorf("expired row: effective=%v state=%s", g.Effective, g.State)
	}
	if !authGrantByID(t, s, live.ID).Effective {
		t.Error("the live row must be effective")
	}

	resp := do(t, s, http.MethodDelete, "/v1/auth/grants/"+live.ID, "tok-root", nil)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if g := authGrantByID(t, s, live.ID); g.Effective || g.State != grantStateRevoked {
		t.Errorf("revoked row: effective=%v state=%s", g.Effective, g.State)
	}
}
