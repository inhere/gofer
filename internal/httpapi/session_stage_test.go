package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/stagenote"
)

type sessionStageBody struct {
	Stage *stagenote.Brief `json:"stage"`
}

func sessionStageOf(t *testing.T, s *Server, method, path string, body any) (*stagenote.Brief, int) {
	t.Helper()
	resp := do(t, s, method, path, testToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d", method, path, resp.StatusCode)
	}
	var raw json.RawMessage
	decode(t, resp, &raw)
	// GET /v1/sessions/{sid} wraps the view as {session: ...}; register / heartbeat answer it bare.
	var wrapped struct {
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Session) > 0 {
		raw = wrapped.Session
	}
	var v sessionStageBody
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	size := 0
	if v.Stage != nil {
		var probe struct {
			Stage json.RawMessage `json:"stage"`
		}
		_ = json.Unmarshal(raw, &probe)
		size = len(probe.Stage)
	}
	return v.Stage, size
}

// The session view the hooks read on every heartbeat carries where the session's flow
// stands: {enabled} before any flow, the step once started, the blocked reason (cut to the
// byte budget) when blocked — and the list stays free of it (one query per session).
func TestSessionViewCarriesTheStageBrief(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSessionInProject(t, s, "sess-stg-aaaa", "/ws/a", "self")

	b, _ := sessionStageOf(t, s, http.MethodGet, "/v1/sessions/sess-stg-aaaa", nil)
	if b == nil || !b.Enabled || b.InFlow() {
		t.Fatalf("before a flow: %+v", b)
	}

	resp := stageDo(t, s, http.MethodPost, "/v1/work-items/stage", testToken,
		map[string]any{"issue": "gofer-21", "template": "small", "project": "self"}, map[string]string{sessionHeader: "sess-stg-aaaa"})
	var v stageViewBody
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start = %d", resp.StatusCode)
	}
	decode(t, resp, &v)

	// the heartbeat answer is the same view
	b, _ = sessionStageOf(t, s, http.MethodPost, "/v1/sessions/sess-stg-aaaa/heartbeat", map[string]any{"event": "UserPromptSubmit", "title": "x"})
	if b == nil || b.WorkItemID != v.WorkItemID || b.Step != "doing" || b.StepNo != 1 || b.StepTotal != 3 || b.Status != "active" {
		t.Fatalf("heartbeat stage: %+v", b)
	}

	stageCall(t, s, http.MethodPost, "/v1/work-items/"+v.WorkItemID+"/stage/blocked",
		map[string]any{"reason": strings.Repeat("等评审", 300)}, http.StatusOK, nil)
	b, size := sessionStageOf(t, s, http.MethodGet, "/v1/sessions/sess-stg-aaaa", nil)
	if b == nil || b.Status != "blocked" || b.Reason == "" || size > stagenote.MaxBytes {
		t.Fatalf("blocked stage: %+v (%d bytes)", b, size)
	}

	resp = do(t, s, http.MethodGet, "/v1/sessions", testToken, nil)
	var listed struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	decode(t, resp, &listed)
	list := listed.Sessions
	if len(list) == 0 {
		t.Fatal("list is empty")
	}
	for _, raw := range list {
		if strings.Contains(string(raw), `"stage"`) {
			t.Fatalf("the session list must not carry stage: %s", raw)
		}
	}
}
