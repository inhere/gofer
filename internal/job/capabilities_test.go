package job

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// capsCfg builds a config with a local + a type=worker runner (remote-w1 → w1) and
// the given project/agent key sets, so the table below can flip one axis at a time.
func capsCfg(projects, agents []string) *config.Config {
	cfg := &config.Config{
		Projects: map[string]config.ProjectConfig{},
		Agents:   map[string]config.AgentConfig{},
		Runners: map[string]config.RunnerConfig{
			"remote-w1":  {Type: "worker", WorkerID: "w1"},
			"remote-any": {Type: "worker"}, // worker runner with NO default worker_id
			"peer-a":     {Type: "peer-http"},
		},
	}
	for _, p := range projects {
		cfg.Projects[p] = config.ProjectConfig{}
	}
	for _, a := range agents {
		cfg.Agents[a] = config.AgentConfig{}
	}
	return cfg
}

// TestCapabilitiesFor (federation P2 / T2.3): the capability view a job is validated
// against must come from the side that EXECUTES it — this host's config for a local
// runner, the worker's register-time report for a worker runner.
func TestCapabilitiesFor(t *testing.T) {
	// One connected worker (w1) with its own, deliberately different, capability set;
	// w2 is connected too so the explicit-override case has a second target.
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w1", Projects: []string{"wproj"}, Agents: []string{"codex", "exec"}},
		{WorkerID: "w2", Projects: []string{"w2proj"}, Agents: []string{"claude"}},
	}}

	cases := []struct {
		name         string
		cfg          *config.Config
		sel          WorkerSelector
		runner       string
		explicitWID  string
		wantProjects string // comma-joined
		wantAgents   string
		wantOnline   bool
	}{
		{
			name:         "local runner returns the host's global config keys",
			cfg:          capsCfg([]string{"beta", "alpha"}, []string{"claude"}),
			sel:          sel,
			runner:       builtinLocalRunner,
			wantProjects: "alpha,beta", // sorted, not map order
			wantAgents:   "claude,exec",
			wantOnline:   true,
		},
		{
			// The built-in exec agent resolves even when undeclared (agent.ResolveAgent),
			// so the local view MUST report it — otherwise P3 would reject a perfectly
			// legal exec job on the local runner.
			name:         "local runner reports built-in exec with an empty agents block",
			cfg:          capsCfg([]string{"alpha"}, nil),
			sel:          sel,
			runner:       builtinLocalRunner,
			wantProjects: "alpha",
			wantAgents:   "exec",
			wantOnline:   true,
		},
		{
			name:         "worker runner returns the ONLINE worker's reported caps (not the host's)",
			cfg:          capsCfg([]string{"alpha"}, []string{"claude"}),
			sel:          sel,
			runner:       "remote-w1",
			wantProjects: "wproj",
			wantAgents:   "codex,exec",
			wantOnline:   true,
		},
		{
			name:        "explicit worker_id overrides the runner's configured default",
			cfg:         capsCfg([]string{"alpha"}, []string{"claude"}),
			sel:         sel,
			runner:      "remote-w1", // configured default is w1 …
			explicitWID: "w2",        // … but the request pins w2
			// Would be "wproj"/"codex,exec" if the default won.
			wantProjects: "w2proj",
			wantAgents:   "claude",
			wantOnline:   true,
		},
		{
			name:       "offline worker has no capability view",
			cfg:        capsCfg([]string{"alpha"}, []string{"claude"}),
			sel:        fakeSelector{}, // nobody connected
			runner:     "remote-w1",
			wantOnline: false,
		},
		{
			name:        "explicit but unregistered worker_id is offline",
			cfg:         capsCfg([]string{"alpha"}, []string{"claude"}),
			sel:         sel,
			runner:      "remote-w1",
			explicitWID: "w9",
			wantOnline:  false,
		},
		{
			name:       "worker runner with no worker_id at all is offline",
			cfg:        capsCfg([]string{"alpha"}, []string{"claude"}),
			sel:        sel,
			runner:     "remote-any",
			wantOnline: false,
		},
		{
			name:       "nil selector (no hub wired) leaves a worker runner offline",
			cfg:        capsCfg([]string{"alpha"}, []string{"claude"}),
			sel:        nil,
			runner:     "remote-w1",
			wantOnline: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{workers: tc.sel}
			caps, online := s.capabilitiesFor(tc.cfg, tc.runner, tc.explicitWID)
			projects, agents := caps.Projects, caps.Agents
			if online != tc.wantOnline {
				t.Fatalf("online = %v, want %v", online, tc.wantOnline)
			}
			if !online {
				if projects != nil || agents != nil {
					t.Fatalf("offline view must be empty, got %v / %v", projects, agents)
				}
				return
			}
			if got := strings.Join(projects, ","); got != tc.wantProjects {
				t.Errorf("projects = %q, want %q", got, tc.wantProjects)
			}
			if got := strings.Join(agents, ","); got != tc.wantAgents {
				t.Errorf("agents = %q, want %q", got, tc.wantAgents)
			}
		})
	}
}

// TestCapabilitiesForNilSelectorLocal: a local runner needs no hub — the host's own
// config is the authority, so a nil selector must NOT make it look offline.
func TestCapabilitiesForNilSelectorLocal(t *testing.T) {
	s := &Service{}
	caps, online := s.capabilitiesFor(capsCfg([]string{"alpha"}, []string{"claude"}), builtinLocalRunner, "")
	projects, agents := caps.Projects, caps.Agents
	if !online {
		t.Fatal("local runner must always be online")
	}
	if strings.Join(projects, ",") != "alpha" || strings.Join(agents, ",") != "claude,exec" {
		t.Fatalf("local caps = %v / %v", projects, agents)
	}
}
