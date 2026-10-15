package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// gofer-ka6r: the plan's supervisor binding. The plan's owner is whoever created
// it — often an agent — so after a token split the person on the web is a
// different caller and could not fix the binding; and the failure said only
// "unknown or not owned by the authenticated caller", which does not tell the
// operator whether the session was missing, theirs, or somebody else's.

func createBoundPlanAs(t *testing.T, s *Server, token, planID, sid string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/plans", token, map[string]any{
		"plan_id": planID, "title": planID, "project": "self", "supervisor_session_id": sid,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create bound plan %s as %s: status=%d (%s)", planID, token, resp.StatusCode, errText(t, resp))
	}
	resp.Body.Close()
}

// TestAdminMayBindAnotherCallersSessionAsPlanSupervisor: a caller holding
// can_admin may rebind a plan its own owner created, to a session that belongs to
// a THIRD caller — while a caller without the capability still may not touch
// somebody else's plan, and the owner keeps working as before.
func TestAdminMayBindAnotherCallersSessionAsPlanSupervisor(t *testing.T) {
	t.Parallel()
	s := newSplitTokenServer(t)
	registerSessionAs(t, s, "tok-agent", "sid-agent-sup")
	registerSessionAs(t, s, "tok-default", "sid-admin-sup")
	createBoundPlanAs(t, s, "tok-agent", "plan-owned-by-agent", "sid-agent-sup")

	// The admin (can_admin) rebinds another caller's plan to its own session.
	resp := do(t, s, http.MethodPatch, "/v1/plans/plan-owned-by-agent", "tok-default",
		map[string]any{"supervisor_session_id": "sid-admin-sup"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin rebind status=%d, want 200 (%s)", resp.StatusCode, errText(t, resp))
	}
	var plan struct {
		SupervisorSessionID string `json:"supervisor_session_id"`
	}
	decode(t, resp, &plan)
	if plan.SupervisorSessionID != "sid-admin-sup" {
		t.Fatalf("supervisor after admin rebind=%q, want sid-admin-sup", plan.SupervisorSessionID)
	}

	// The owner keeps its own right (agent-dev, no can_admin).
	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-owned-by-agent", "tok-agent",
		map[string]any{"supervisor_session_id": "sid-agent-sup"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner rebind status=%d, want 200 (%s)", resp.StatusCode, errText(t, resp))
	}
	resp.Body.Close()

	// A caller with neither ownership nor can_admin is still refused.
	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-owned-by-agent", "tok-other",
		map[string]any{"supervisor_session_id": "sid-agent-sup"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger rebind status=%d, want 403 (%s)", resp.StatusCode, errText(t, resp))
	}
	// The refusal names the permission that would allow it (仅根), which is the permission
	// form of the can_admin bit this check used to ask for (plan T1.5d).
	text := errText(t, resp)
	if !strings.Contains(text, "plan.supervise.others") {
		t.Fatalf("refusal does not say what would allow it: %q", text)
	}
}

// TestSupervisorBindingNamesBothCallers: a non-admin owner that names another
// caller's session gets told which session, whose it is and which caller is
// asking — and that an admin can hand it over.
func TestSupervisorBindingNamesBothCallers(t *testing.T) {
	t.Parallel()
	s := newSplitTokenServer(t)
	registerSessionAs(t, s, "tok-default", "sid-default-sup")
	registerSessionAs(t, s, "tok-agent", "sid-agent-sup")
	createBoundPlanAs(t, s, "tok-agent", "plan-msg-check", "sid-agent-sup")

	resp := do(t, s, http.MethodPatch, "/v1/plans/plan-msg-check", "tok-agent",
		map[string]any{"supervisor_session_id": "sid-default-sup"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign session bind status=%d, want 400 (%s)", resp.StatusCode, errText(t, resp))
	}
	text := errText(t, resp)
	for _, want := range []string{"sid-default-sup", "default", "agent-dev", "gofer session owner"} {
		if !strings.Contains(text, want) {
			t.Fatalf("refusal %q does not mention %q", text, want)
		}
	}

	// An unregistered session is a different story, and says so.
	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-msg-check", "tok-agent",
		map[string]any{"supervisor_session_id": "sid-not-here"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown session bind status=%d, want 400 (%s)", resp.StatusCode, errText(t, resp))
	}
	if text := errText(t, resp); !strings.Contains(text, "not registered") {
		t.Fatalf("refusal for an unknown session is unclear: %q", text)
	}
}
