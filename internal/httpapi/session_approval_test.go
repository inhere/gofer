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
