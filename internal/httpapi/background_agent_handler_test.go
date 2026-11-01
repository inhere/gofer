package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/jobstore"
)

func seedBackgroundSession(t *testing.T, s *Server, kind, project, sid string, used, limit int64) {
	t.Helper()
	err := s.jobs.Meta().ReplaceBackgroundSession(jobstore.BackgroundSession{
		Kind: kind, SessionID: sid, Agent: "a1", Project: project, Model: "m1",
		CreatedAt: 1000, LastUsedAt: 2000, Runs: 3, ContextUsed: used, ContextLimit: limit, State: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func kindOf(v bgagent.View, kind string) (bgagent.KindView, bool) {
	for _, k := range v.Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return bgagent.KindView{}, false
}

// TestBackgroundAgentsListShape: a fresh server answers every kind with an empty session
// list (not null, no invented session), the version field, and a policy.
func TestBackgroundAgentsListShape(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/background-agents", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", resp.StatusCode)
	}
	body := readBody(t, resp)
	var v bgagent.View
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if v.Version != bgagent.ViewVersion || len(v.Kinds) != len(bgagent.Kinds()) {
		t.Fatalf("view = %s", body)
	}
	for _, k := range v.Kinds {
		if k.Sessions == nil || len(k.Sessions) != 0 || k.Today.Runs != 0 {
			t.Errorf("%s on a fresh server = %+v, want no sessions and no runs", k.Kind, k)
		}
		if k.Policy.MaxRuns <= 0 || k.Policy.MaxContextRatio <= 0 {
			t.Errorf("%s policy = %+v, want the defaults", k.Kind, k.Policy)
		}
	}
}

// TestBackgroundAgentsListShowsSessions: a recorded session shows with its context, an
// unknown context is null, other kinds stay empty.
func TestBackgroundAgentsListShowsSessions(t *testing.T) {
	s := newTestServer(t, testToken, false)
	seedBackgroundSession(t, s, bgagent.KindSteward, "p1", "sess-1", 30, 100)
	seedBackgroundSession(t, s, bgagent.KindSteward, "p2", "sess-2", 0, 0)

	var v bgagent.View
	resp := do(t, s, http.MethodGet, "/v1/background-agents", testToken, nil)
	decode(t, resp, &v)
	k, ok := kindOf(v, bgagent.KindSteward)
	if !ok || len(k.Sessions) != 2 {
		t.Fatalf("steward = %+v", k)
	}
	for _, sv := range k.Sessions {
		switch sv.ID {
		case "sess-1":
			if sv.ContextRatio == nil || *sv.ContextRatio != 0.3 {
				t.Errorf("sess-1 ratio = %v, want 0.3", sv.ContextRatio)
			}
		case "sess-2":
			if sv.ContextRatio != nil || sv.ContextUsedTokens != nil || sv.ContextLimitTokens != nil {
				t.Errorf("sess-2 context = %+v, want null (unknown), not 0", sv)
			}
		default:
			t.Errorf("unexpected session %s", sv.ID)
		}
	}
	if other, _ := kindOf(v, bgagent.KindJobCheck); len(other.Sessions) != 0 {
		t.Errorf("job_check sessions = %d, want 0", len(other.Sessions))
	}
}

// TestBackgroundAgentSessionReset: the reset ends the kind's active sessions, answers with the
// card as it reads afterwards, audits, and leaves other kinds alone.
func TestBackgroundAgentSessionReset(t *testing.T) {
	s := newTestServer(t, testToken, false)
	seedBackgroundSession(t, s, bgagent.KindSteward, "p1", "sess-1", 30, 100)
	seedBackgroundSession(t, s, bgagent.KindSummarizer, "p1", "sess-2", 30, 100)

	resp := do(t, s, http.MethodPost, "/v1/background-agents/steward/session/reset", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Version int              `json:"version"`
		Reset   int64            `json:"reset"`
		Kind    bgagent.KindView `json:"kind"`
	}
	decode(t, resp, &out)
	if out.Reset != 1 || out.Kind.Kind != bgagent.KindSteward || len(out.Kind.Sessions) != 0 || out.Version != bgagent.ViewVersion {
		t.Fatalf("reset answer = %+v", out)
	}
	var v bgagent.View
	decode(t, do(t, s, http.MethodGet, "/v1/background-agents", testToken, nil), &v)
	if k, _ := kindOf(v, bgagent.KindSummarizer); len(k.Sessions) != 1 {
		t.Errorf("summarizer sessions = %d, want 1 (untouched)", len(k.Sessions))
	}
	rows, err := s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{Subject: bgagent.KindSteward})
	if err != nil || len(rows) != 1 || rows[0].Action != "background_agent.reset" || rows[0].Reason != "sessions=1" {
		t.Fatalf("auth audit = %+v, %v", rows, err)
	}
	if evs, err := s.jobs.Meta().ListAuditEvents("background_agent.reset", "steward:p1"); err != nil || len(evs) != 1 {
		t.Fatalf("runner audit events = %d, %v", len(evs), err)
	}
}

func TestBackgroundAgentSessionResetUnknownKind(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/background-agents/nope/session/reset", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown kind = %d, want 404", resp.StatusCode)
	}
}

// TestBackgroundAgentsPermissionsAndCeiling: reading needs settings.read, resetting
// settings.write (not seeded to a plain caller), and a caller with a project ceiling is
// refused both, even with every permission.
func TestBackgroundAgentsPermissionsAndCeiling(t *testing.T) {
	s := newCeilingServer(t)
	if st, body := ceilReq(t, s, ceilTokPlain, http.MethodPost, "/v1/background-agents/steward/session/reset", nil); st != http.StatusForbidden {
		t.Fatalf("reset without settings.write = %d (%s), want 403", st, body)
	}
	grantAll(s)
	if st, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/background-agents", nil); st != http.StatusOK {
		t.Fatalf("plain GET = %d (%s), want 200", st, body)
	}
	if st, body := ceilReq(t, s, ceilTokPlain, http.MethodPost, "/v1/background-agents/steward/session/reset", nil); st != http.StatusOK {
		t.Fatalf("plain reset = %d (%s), want 200", st, body)
	}
	if st, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/background-agents", nil); st != http.StatusForbidden {
		t.Fatalf("ceiling GET = %d (%s), want 403", st, body)
	}
	if st, body := ceilReq(t, s, ceilTokLim, http.MethodPost, "/v1/background-agents/steward/session/reset", nil); st != http.StatusForbidden {
		t.Fatalf("ceiling reset = %d (%s), want 403", st, body)
	}
}
