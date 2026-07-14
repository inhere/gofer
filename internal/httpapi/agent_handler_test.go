package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// countingDetector reports `ghost` available and counts the detect passes it runs.
type countingDetector struct{ calls int }

func (d *countingDetector) Detect(agents map[string]config.AgentConfig) map[string]agent.DetectResult {
	d.calls++
	out := make(map[string]agent.DetectResult, len(agents))
	for key := range agents {
		switch key {
		case "ghost":
			out[key] = agent.DetectResult{Available: true, Version: "9.9.9"}
		case "exec":
			out[key] = agent.DetectResult{Available: true, Version: "builtin"}
		}
	}
	return out
}

// TestListAgentsServesCachedAvailability: GET /v1/agents reads the availability cache
// (agent.Registry.Availability) instead of probing every agent on every request — it
// used to spawn one `--version` child process PER AGENT PER REQUEST, so a browser
// refresh cost N processes, and P2's template injection only grows N.
//
// Two independent proofs that no probe runs per request:
//   - the detect pass count stays at the ONE core.Build paid for, across 3 requests;
//   - `ghost` is not on PATH, so a live probe could only report it unavailable —
//     reading back available=true with the seeded version can only come from the cache.
func TestListAgentsServesCachedAvailability(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Agents: map[string]config.AgentConfig{
			"ghost": {Type: agent.TypeCLIAgent, Command: "gofer-ghost-cli-not-installed", Args: []string{"{{prompt}}"}},
		},
	}
	// Resolve + seed the registry exactly as core.Build does (one detect pass, results
	// handed to the registry).
	det := &countingDetector{}
	cfg, detected := agent.Resolve(cfg, det)
	if det.calls != 1 {
		t.Fatalf("assembly ran %d detect passes, want 1", det.calls)
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistryWith(cfg, detected)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	s := New(&cfg.Server, testToken, false, jobs, workflow.NewEngine(jobs), projects, agents, nil, nil, nil, nil)

	for i := range 3 {
		resp := do(t, s, http.MethodGet, "/v1/agents", testToken, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status=%d, want 200", i, resp.StatusCode)
		}
		var body struct {
			Agents []agentView `json:"agents"`
		}
		decode(t, resp, &body)

		views := map[string]agentView{}
		for _, a := range body.Agents {
			views[a.Key] = a
		}
		if got := views["ghost"]; !got.Available || got.Version != "9.9.9" {
			t.Fatalf("request %d: ghost = %+v, want the cached {available true, version 9.9.9} (a live probe would say unavailable)", i, got)
		}
		if got := views["exec"]; !got.Available {
			t.Fatalf("request %d: built-in exec must stay available, got %+v", i, got)
		}
	}
	if det.calls != 1 {
		t.Fatalf("3 requests to /v1/agents ran %d detect passes, want the 1 from assembly", det.calls)
	}
}
