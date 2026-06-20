package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newMetaServer builds a Server exercising the /v1/meta aggregate: a project with
// allowlists + default_agent, two agent types (cli-agent + exec), a worker runner
// and configured workers, with an injected workerRegistry for connected/labels.
func newMetaServer(t *testing.T, workers workerRegistry) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token: testToken,
			Workers: map[string]config.WorkerAuthConfig{
				"w-online":  {Token: "tok-online"},
				"w-offline": {Token: "tok-offline"},
			},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"proj-a": {
				HostPath:       root,
				DefaultAgent:   "codex",
				AllowedAgents:  []string{"codex", "exec"},
				AllowedRunners: []string{"local", "w"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: "cli-agent"},
			"exec":  {Type: "exec"},
		},
		Runners: map[string]config.RunnerConfig{
			"w": {Type: "worker", WorkerID: "w-online"},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	return New(&cfg.Server, testToken, false, jobs, projects, agents, nil, cfg.Runners, nil, workers)
}

// getMeta GETs /v1/meta and decodes the aggregate.
func getMeta(t *testing.T, s *Server) metaResp {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/meta", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/meta status=%d, want 200", resp.StatusCode)
	}
	var out metaResp
	decode(t, resp, &out)
	return out
}

func TestMetaRequiresAuth(t *testing.T) {
	s := newMetaServer(t, nil)
	resp := do(t, s, http.MethodGet, "/v1/meta", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-bearer status=%d, want 401", resp.StatusCode)
	}
}

// TestMetaGroupsNonEmpty: every group is a non-nil populated array and the
// project carries its allowlists + default_agent.
func TestMetaGroups(t *testing.T) {
	workers := fakeWorkers{
		"w-online": {Connected: true, LastHeartbeat: 1750300000000, InFlight: 1, Labels: []string{"gpu", "linux"}},
	}
	m := getMeta(t, newMetaServer(t, workers))

	if len(m.Projects) != 1 {
		t.Fatalf("want 1 project, got %d: %+v", len(m.Projects), m.Projects)
	}
	p := m.Projects[0]
	if p.Key != "proj-a" || p.DefaultAgent != "codex" {
		t.Fatalf("project meta wrong: %+v", p)
	}
	if len(p.AllowedAgents) != 2 || len(p.AllowedRunners) != 2 {
		t.Fatalf("project allowlists wrong: %+v", p)
	}

	agentTypes := map[string]string{}
	for _, a := range m.Agents {
		agentTypes[a.Key] = a.Type
	}
	if agentTypes["codex"] != "cli-agent" || agentTypes["exec"] != "exec" {
		t.Fatalf("agent meta types wrong: %+v", m.Agents)
	}

	runnerTypes := map[string]string{}
	for _, r := range m.Runners {
		runnerTypes[r.Name] = r.Type
	}
	if m.Runners[0].Name != "local" {
		t.Fatalf("local runner must be first: %+v", m.Runners)
	}
	if runnerTypes["w"] != "worker" {
		t.Fatalf("worker runner missing: %+v", m.Runners)
	}

	if len(m.Workers) != 2 {
		t.Fatalf("want 2 workers, got %d: %+v", len(m.Workers), m.Workers)
	}
}

// TestMetaWorkerConnectedMatchesRunners: the connected/labels state surfaced by
// /v1/meta for a worker agrees with what /v1/runners reports (same source).
func TestMetaWorkerConnectedMatchesRunners(t *testing.T) {
	workers := fakeWorkers{
		"w-online": {Connected: true, LastHeartbeat: 1750300000000, InFlight: 1, Labels: []string{"gpu", "linux"}},
	}
	s := newMetaServer(t, workers)
	m := getMeta(t, s)

	byID := map[string]metaWorker{}
	for _, w := range m.Workers {
		byID[w.ID] = w
	}
	online, offline := byID["w-online"], byID["w-offline"]
	if !online.Connected || len(online.Labels) != 2 {
		t.Fatalf("w-online meta should be connected with labels: %+v", online)
	}
	if offline.Connected || len(offline.Labels) != 0 {
		t.Fatalf("w-offline meta should be disconnected with no labels: %+v", offline)
	}

	// Cross-check against /v1/runners: the worker runner "w" targets w-online and
	// must read `connected` there too (same WorkerStatus source).
	rows := byName(listRunners(t, s))
	wr := rows["w"]
	if wr.Status != statusConnected {
		t.Fatalf("/v1/runners worker status=%q, want connected (meta said connected=%v)", wr.Status, online.Connected)
	}
	if (wr.Status == statusConnected) != online.Connected {
		t.Fatalf("meta vs /v1/runners connected mismatch: meta=%v runners=%q", online.Connected, wr.Status)
	}
}

// TestMetaWorkersNilRegistry: with no workers registry wired, configured workers
// still list (from config) but report connected=false / no labels.
func TestMetaWorkersNilRegistry(t *testing.T) {
	m := getMeta(t, newMetaServer(t, nil))
	if len(m.Workers) != 2 {
		t.Fatalf("want 2 configured workers, got %d", len(m.Workers))
	}
	for _, w := range m.Workers {
		if w.Connected || len(w.Labels) != 0 {
			t.Fatalf("nil registry worker should be disconnected: %+v", w)
		}
	}
}
