package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// wakeupPendingServer serves one wakeup whose receipt already triggered and waits for its
// resident session (pending_delivery, no job id).
func wakeupPendingServer(t *testing.T) *httptest.Server {
	t.Helper()
	w := client.Wakeup{
		ID: "wk-1", JobID: "job-1", Kind: "event", Mode: "continuous", Enabled: true,
		EventTypes: []string{"job.terminal"}, PendingDelivery: true,
	}
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/jobs/job-1/wakeups":
			_ = json.NewEncoder(rw).Encode(client.WakeupsResp{Wakeups: []client.Wakeup{w}})
		case "/v1/wakeups/wk-1":
			_ = json.NewEncoder(rw).Encode(w)
		default:
			http.NotFound(rw, r)
		}
	}))
}

// TestWakeupShowsPendingDelivery: `job wakeup ls` and `wakeup show` say the receipt is waiting
// for its resident session; neither prints the stored sentinel where a job id belongs.
func TestWakeupShowsPendingDelivery(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	resetWakeupFlagGlobals()
	t.Cleanup(resetWakeupFlagGlobals)

	ts := wakeupPendingServer(t)
	defer ts.Close()
	run := func(args ...string) (string, int) {
		t.Helper()
		var code int
		out := captureOutput(t, func() { code = NewApp("test").Run(append(args, "--server", ts.URL)) })
		return out, code
	}

	out, code := run("job", "wakeup", "ls", "job-1")
	if code != 0 {
		t.Fatalf("wakeup ls exit code=%d, out=%s", code, out)
	}
	if !strings.Contains(out, "pending delivery") {
		t.Errorf("wakeup ls output = %q, want it to mark the pending delivery", out)
	}
	if strings.Contains(out, "await_input") {
		t.Errorf("wakeup ls output = %q, want no sentinel", out)
	}

	out, code = run("job", "wakeup", "show", "wk-1")
	if code != 0 {
		t.Fatalf("wakeup show exit code=%d, out=%s", code, out)
	}
	if !strings.Contains(out, "pending:") || !strings.Contains(out, "waits for the session to be idle") {
		t.Errorf("wakeup show output = %q, want a pending-delivery line", out)
	}
	if strings.Contains(out, "continuation:") || strings.Contains(out, "await_input") {
		t.Errorf("wakeup show output = %q, want no job id for a pending delivery", out)
	}
}
