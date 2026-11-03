package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobRunParentWakeFlagsReachTheRequest drives the REAL entry path (assembly +
// app.Run): the two child-receipt flags of `job run` must end up in the submitted
// request body the server receives (GR110 — the CLI is tested through its dispatch,
// not through a helper that could drift from the flag it belongs to).
func TestJobRunParentWakeFlagsReachTheRequest(t *testing.T) {
	resetFlagGlobals()
	defer resetFlagGlobals()

	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"job-new","status":"queued"}`))
	}))
	defer srv.Close()

	t.Setenv(config.EnvConfigDir, t.TempDir())
	out := captureOutput(t, func() {
		NewApp("test").Run([]string{
			"job", "run", "-p", "self", "--agent", "exec", "--runner", "local",
			"--no-parent-wake",
			"--parent-wake-events", "job.terminal,interaction.created",
			"--server", srv.URL,
			"--", "go", "version",
		})
	})

	var body map[string]any
	select {
	case body = <-got:
	default:
		t.Fatalf("no submit reached the server, output=%q", out)
	}
	if body["no_parent_wake"] != true {
		t.Fatalf("no_parent_wake = %v, want true (body=%v)", body["no_parent_wake"], body)
	}
	events, _ := body["parent_wake_events"].([]any)
	if len(events) != 2 || events[0] != job.EventJobTerminal || events[1] != job.EventInteractionCreated {
		t.Fatalf("parent_wake_events = %v, want both requested events", body["parent_wake_events"])
	}
}
