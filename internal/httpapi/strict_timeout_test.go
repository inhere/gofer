package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// strict_timeout turns the silent clamp into a 400 that names the limit and its config key;
// without it the response reports the clamp and where the limit came from.
func TestSubmitStrictTimeoutAndClampSource(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken, MaxJobTimeoutSec: 100})
	body := map[string]any{"project_key": "self", "agent": "exec", "runner": "local", "cmd": testcmd.Cmd(t, "env-print"), "timeout_sec": 500, "strict_timeout": true}

	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, body)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "server.max_job_timeout_sec") || !strings.Contains(string(data), "limit 100s") {
		t.Fatalf("strict submit = %d %s, want 400 naming the limit and key", resp.StatusCode, data)
	}

	delete(body, "strict_timeout")
	resp = do(t, s, http.MethodPost, "/v1/jobs", testToken, body)
	var out struct {
		ID                  string `json:"id"`
		TimeoutSec          int    `json:"timeout_sec"`
		RequestedTimeoutSec int    `json:"requested_timeout_sec"`
		TimeoutClamped      bool   `json:"timeout_clamped"`
		TimeoutLimitSource  string `json:"timeout_limit_source"`
		TimeoutLimitKey     string `json:"timeout_limit_key"`
	}
	decode(t, resp, &out)
	if out.TimeoutSec != 100 || out.RequestedTimeoutSec != 500 || !out.TimeoutClamped || out.TimeoutLimitSource != "server" || out.TimeoutLimitKey != "server.max_job_timeout_sec" {
		t.Fatalf("clamped submit = %+v", out)
	}

	// gofer-ea2i: the detail endpoint (what `job show` and the web detail page read) must
	// carry the same fields, otherwise the clamp can only be explained right after submit.
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+out.ID, testToken, nil)
	var detail struct {
		TimeoutSec          int    `json:"timeout_sec"`
		RequestedTimeoutSec int    `json:"requested_timeout_sec"`
		TimeoutClamped      bool   `json:"timeout_clamped"`
		TimeoutLimitSource  string `json:"timeout_limit_source"`
		TimeoutLimitKey     string `json:"timeout_limit_key"`
	}
	decode(t, resp, &detail)
	if resp.StatusCode != http.StatusOK || detail.TimeoutSec != 100 || detail.RequestedTimeoutSec != 500 ||
		!detail.TimeoutClamped || detail.TimeoutLimitSource != "server" || detail.TimeoutLimitKey != "server.max_job_timeout_sec" {
		t.Fatalf("GET /v1/jobs/{id} must keep the clamp fields, got %d %+v", resp.StatusCode, detail)
	}
}
