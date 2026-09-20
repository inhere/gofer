package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// templateDetector reports exactly the listed agent keys as available, so a test can
// drive agent.Resolve's template injection without touching the host's PATH.
type templateDetector struct{ available map[string]bool }

func (d templateDetector) Detect(agents map[string]config.AgentConfig) map[string]agent.DetectResult {
	out := make(map[string]agent.DetectResult, len(agents))
	for key := range agents {
		if d.available[key] {
			out[key] = agent.DetectResult{Available: true, Version: "test-1.0"}
		}
	}
	return out
}

// TestListAgentsMarksTemplateInjected answers the question a reader of the console
// asks first ("claude-acp / omp-acp are in the list but not in my config.yaml —
// where do they come from?"): the entry carries injected=true when it was
// materialized at runtime from a built-in template, and an operator-declared agent
// never does. Without the flag the two kinds are indistinguishable over the API.
func TestListAgentsMarksTemplateInjected(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Agents: map[string]config.AgentConfig{
			// Declared by the operator: never injected, whatever the probe says.
			"mine": {Type: agent.TypeCLIAgent, Command: "echo"},
		},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"mine"}, AllowedRunners: []string{"local"}},
		},
	}
	// The one pass that injects templates is agent.Resolve; a host that has the
	// template's CLI reports it available and the template is merged in.
	cfg, avail := agent.Resolve(cfg, templateDetector{available: map[string]bool{"omp-acp": true}})
	if !cfg.IsInjectedAgent("omp-acp") {
		t.Fatalf("setup: omp-acp was not injected by Resolve (agents=%v)", cfg.Agents)
	}

	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistryWith(cfg, avail)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, filepath.Join(root, "db")), nil))
	eng := workflow.NewEngine(jobs)
	s := New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil)

	resp := do(t, s, http.MethodGet, "/v1/agents", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Agents []struct {
			Key      string `json:"key"`
			Injected bool   `json:"injected"`
		} `json:"agents"`
	}
	decode(t, resp, &out)

	got := map[string]bool{}
	for _, a := range out.Agents {
		got[a.Key] = a.Injected
	}
	if _, listed := got["omp-acp"]; !listed {
		t.Fatalf("omp-acp missing from /v1/agents: %+v", out.Agents)
	}
	if !got["omp-acp"] {
		t.Errorf("omp-acp injected=false, want true (it came from a built-in template)")
	}
	if got["mine"] {
		t.Errorf("mine injected=true, want false (the operator declared it)")
	}
}
