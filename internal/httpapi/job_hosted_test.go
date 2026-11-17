package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The HTTP body carries `hosted` (hosted resident session design §1) exactly like the
// CLI's --hosted: it is the request field itself, so Submit's own rule decides it. This
// case proves the field survives the body → JobRequest hop and that a plain job asking
// for it is refused instead of silently ignored.
func TestJobSubmitHostedFieldReachesSubmit(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, map[string]any{
		"project_key": "self", "agent": "exec", "runner": "local",
		"prompt": "plain job, no session", "hosted": true,
	})
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (hosted without session); body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "hosted requires a persistent session") {
		t.Fatalf("body = %s, want Submit's hosted rule", body)
	}
}
