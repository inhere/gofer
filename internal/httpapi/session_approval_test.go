package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gofer-81b4: a submitted job cannot carry its own ACP approval policy
// (session_approval). The server decides it (mail.agents.<名字>.approval), so a caller that
// sends the field is REFUSED — not silently ignored, which would leave it believing its
// gate took effect, and not honoured, which would let it widen one.
func TestCreateJobRefusesSessionApproval(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, map[string]any{
		"project_key": "self", "agent": "exec", "runner": "local",
		"cmd": []string{"go", "version"}, "cwd": ".",
		"session_approval": map[string]any{"mode": "off"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d (body %s), want %d", resp.StatusCode, body, http.StatusBadRequest)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "session_approval") {
		t.Fatalf("body = %s, want it to name session_approval", body)
	}

	// The markdown frontmatter path is held to the same bar (the yaml tag carries the field
	// so the refusal names it instead of ignoring it).
	md := "---\nproject_key: self\nagent: codex\nrunner: local\nsession_approval:\n  mode: off\n---\n\ndo the thing\n"
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(md))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "text/markdown")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if mdResp := rec.Result(); mdResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("markdown status = %d, want %d", mdResp.StatusCode, http.StatusBadRequest)
	}
}

// permission_tier is the caller's only gate knob and it only tightens: an acp-agent-only
// value on another agent kind is refused at admission (400, naming the field), and sending
// it next to a session_approval does not make the latter acceptable.
func TestCreateJobPermissionTierIsValidatedAndSessionApprovalStillRefused(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, map[string]any{
		"project_key": "self", "agent": "exec", "runner": "local",
		"cmd": []string{"go", "version"}, "cwd": ".", "permission_tier": "ask",
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "permission_tier") {
		t.Fatalf("ask on an exec agent: status = %d body = %s, want 400 naming permission_tier", resp.StatusCode, body)
	}

	resp = do(t, s, http.MethodPost, "/v1/jobs", testToken, map[string]any{
		"project_key": "self", "agent": "exec", "runner": "local",
		"cmd": []string{"go", "version"}, "cwd": ".", "permission_tier": "ask",
		"session_approval": map[string]any{"mode": "off"},
	})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "session_approval") {
		t.Fatalf("session_approval next to a tier: status = %d body = %s, want the session_approval refusal", resp.StatusCode, body)
	}
}
