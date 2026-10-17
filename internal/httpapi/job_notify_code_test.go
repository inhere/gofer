package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestSubmitNotifyTargetRejectionCarriesCode: a submit refused only for its notify
// target answers 400 with the stable code the CLI branches on; other 400s carry none.
func TestSubmitNotifyTargetRejectionCarriesCode(t *testing.T) {
	srv := newTestServer(t, "tok", false)
	post := func(body map[string]any) (int, map[string]any) {
		resp := do(t, srv, http.MethodPost, "/v1/jobs", "tok", body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}
	base := func() map[string]any {
		return map[string]any{"project_key": "self", "agent": "exec", "runner": "local", "cmd": []string{"go", "version"}, "cwd": "."}
	}

	req := base()
	req["notify_session"] = "no-such-session"
	status, out := post(req)
	if status != http.StatusBadRequest || out["code"] != job.ErrCodeNotifyTargetRejected {
		t.Fatalf("status=%d body=%v, want 400 with code %q", status, out, job.ErrCodeNotifyTargetRejected)
	}

	req = base()
	req["agent"] = "no-such-agent"
	status, out = post(req)
	if status == http.StatusOK || out["code"] != nil {
		t.Fatalf("status=%d body=%v, want a rejection without a code", status, out)
	}
}
