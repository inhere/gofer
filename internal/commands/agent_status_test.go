package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// agentHealthStub serves GET /v1/agents with one degraded and one unknown agent, and
// POST /v1/agents/{key}/probe with a configurable outcome.
type agentHealthStub struct {
	probeStatus string
	probeExit   int
}

func (a *agentHealthStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []any{
				map[string]any{
					"key": "codex", "type": "cli-agent", "available": true, "version": "1.2.3",
					"health": map[string]any{
						"state": "degraded", "window_sec": 3600, "jobs": 4, "ok": 1,
						"transient_fail": 3, "last_transient_at": 1_700_000_100, "last_ok_at": 1_700_000_050,
					},
				},
				map[string]any{
					"key": "omp", "type": "cli-agent", "available": true, "version": "0.9",
					"health": map[string]any{"state": "unknown", "window_sec": 3600},
				},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/probe"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"job_id": "20260918-120000-abcd1234", "status": a.probeStatus, "exit_code": a.probeExit,
				"duration_ms": 1234, "first_line": "OK",
			})
		default:
			http.NotFound(w, r)
		}
	})
}

// stubAgentServer points the shared connection flags at a stub server for one test and
// restores them afterwards.
func stubAgentServer(t *testing.T, h http.Handler) {
	t.Helper()
	t.Setenv("GOFER_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jobConnOpts.server = srv.URL
	jobConnOpts.token = ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
}

// TestAgentStatusPrintsHealth: `agent status` renders the availability cache TOGETHER
// with the recent-job health — the operator sees which agent is degraded, over how
// many jobs, and when the provider last failed, in one read.
func TestAgentStatusPrintsHealth(t *testing.T) {
	stub := &agentHealthStub{probeStatus: "done"}
	stubAgentServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAgentCmd(), "status"))
	out := captureOutput(t, func() {
		if err := runAgentStatus(c, nil); err != nil {
			t.Fatalf("agent status: %v", err)
		}
	})

	for _, want := range []string{"codex", "cli-agent", "degraded", "3", "1", "omp", "unknown"} {
		if !strings.Contains(out, want) {
			t.Fatalf("agent status output missing %q:\n%s", want, out)
		}
	}
}

// TestAgentProbeCommand: `agent probe <key>` submits the probe and prints its outcome,
// exiting non-zero when the probe itself failed (so a script can gate on it).
func TestAgentProbeCommand(t *testing.T) {
	stub := &agentHealthStub{probeStatus: "done"}
	stubAgentServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAgentCmd(), "probe"))
	c.Arg("key").WithValue("codex")
	out := captureOutput(t, func() {
		if err := runAgentProbe(c, nil); err != nil {
			t.Fatalf("agent probe: %v", err)
		}
	})
	for _, want := range []string{"20260918-120000-abcd1234", "done", "OK"} {
		if !strings.Contains(out, want) {
			t.Fatalf("probe output missing %q:\n%s", want, out)
		}
	}

	// A probe that came back failed must exit non-zero: that is the whole point of
	// running it from a script.
	failed := &agentHealthStub{probeStatus: "failed", probeExit: 7}
	stubAgentServer(t, failed.handler())
	c2 := bindCmd(findSub(t, NewAgentCmd(), "probe"))
	c2.Arg("key").WithValue("codex")
	var err error
	_ = captureOutput(t, func() { err = runAgentProbe(c2, nil) })
	if err == nil {
		t.Fatal("a failed probe must be reported as an error (non-zero exit)")
	}
	assertCodedExit(t, err)
}
