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
		case r.Method == http.MethodGet && r.URL.Path == "/v1/stats":
			// 用量块（SUP-01 E）：codex 在 24h 窗口里有结算，omp 没有（两列应为 `-`）。
			codexUsage := map[string]any{
				"jobs": 4, "total_tokens": 305145, "input_tokens": 12345, "output_tokens": 3800, "cost_usd": 0.0032,
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"usage": map[string]any{
				"windows": map[string]any{
					"24h": map[string]any{"by_agent": map[string]any{"codex": codexUsage}, "total": codexUsage},
					"7d":  map[string]any{"by_agent": map[string]any{"codex": codexUsage}, "total": codexUsage},
				},
				"partial": false,
			}})
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

// TestAgentStatusPrintsUsage: the table carries each agent's 24h token/cost total
// (SUP-01 E) beside its health — one read answers both "is this agent healthy?" and
// "what did it burn today?". An agent with no usage in the window shows `-`, not 0.
func TestAgentStatusPrintsUsage(t *testing.T) {
	stub := &agentHealthStub{}
	stubAgentServer(t, stub.handler())

	c := bindCmd(findSub(t, NewAgentCmd(), "status"))
	out := captureOutput(t, func() {
		if err := runAgentStatus(c, nil); err != nil {
			t.Fatalf("agent status: %v", err)
		}
	})

	for _, want := range []string{"24H_TOKENS", "24H_COST", "305k", "$0.0032"} {
		if !strings.Contains(out, want) {
			t.Fatalf("agent status output missing %q:\n%s", want, out)
		}
	}
	// omp 在窗口内没有用量 → 两列都是 `-`（不是 0：没报 ≠ 0）。
	ompLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "omp ") {
			ompLine = line
		}
	}
	if ompLine == "" {
		t.Fatalf("agent status printed no omp row:\n%s", out)
	}
	if !strings.Contains(ompLine, "-") || strings.Contains(ompLine, "$") {
		t.Fatalf("omp row = %q, want `-` for an agent with no usage in the window", ompLine)
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
