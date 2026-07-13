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
			"codex":  {Type: "cli-agent"},
			"claude": {Type: "cli-agent", Interactive: true},
			"exec":   {Type: "exec"},
		},
		Runners: map[string]config.RunnerConfig{
			"w": {Type: "worker", WorkerID: "w-online"},
		},
	}
	return wireMetaServer(t, cfg, workers)
}

// wireMetaServer wires a Server from an already-built config (shared by the fixtures
// so a test can vary the config — e.g. omit the exec agent to exercise built-in
// resolution). cfg.Storage.Root must be set (the backing store lives under it).
func wireMetaServer(t *testing.T, cfg *config.Config, workers workerRegistry) *Server {
	t.Helper()
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, cfg.Storage.Root), nil)
	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	return New(&cfg.Server, testToken, false, jobs, jobsEng, projects, agents, nil, cfg.Runners, nil, workers)
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
		"w-online": {
			Connected:     true,
			LastHeartbeat: 1750300000000,
			InFlight:      1,
			Labels:        []string{"gpu", "linux"},
			Projects:      []string{"proj-a"},
			Agents:        []string{"codex"},
		},
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
		"w-online": {
			Connected:     true,
			LastHeartbeat: 1750300000000,
			InFlight:      1,
			Labels:        []string{"gpu", "linux"},
			Projects:      []string{"proj-a"},
			Agents:        []string{"codex"},
		},
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
	if len(online.Projects) != 1 || online.Projects[0] != "proj-a" ||
		len(online.Agents) != 1 || online.Agents[0] != "codex" {
		t.Fatalf("w-online meta projects/agents wrong: %+v", online)
	}
	if offline.Connected || len(offline.Labels) != 0 || len(offline.Projects) != 0 || len(offline.Agents) != 0 {
		t.Fatalf("w-offline meta should be disconnected with no metadata: %+v", offline)
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
	if len(wr.Worker.Projects) != len(online.Projects) || len(wr.Worker.Agents) != len(online.Agents) {
		t.Fatalf("meta vs /v1/runners projects/agents mismatch: meta=%+v runners=%+v", online, wr.Worker)
	}
}

// TestMetaAgentInteractive (P4 T4.2): metaAgent carries the resolved interactive
// flag — true for an interactive cli-agent, absent/false for others — and the
// built-in exec is present with type exec.
func TestMetaAgentInteractive(t *testing.T) {
	m := getMeta(t, newMetaServer(t, nil))
	byKey := map[string]metaAgent{}
	for _, a := range m.Agents {
		byKey[a.Key] = a
	}
	if a, ok := byKey["claude"]; !ok || a.Type != "cli-agent" || !a.Interactive {
		t.Fatalf("claude should be interactive cli-agent: %+v", byKey["claude"])
	}
	if a := byKey["codex"]; a.Interactive {
		t.Fatalf("codex should not be interactive: %+v", a)
	}
	if a, ok := byKey["exec"]; !ok || a.Type != "exec" || a.Interactive {
		t.Fatalf("exec should be present, type exec, non-interactive: %+v", byKey["exec"])
	}
}

// TestMetaAgentExecBuiltin (P4 T4.2): the local agent set comes from the RESOLVED
// registry, so the built-in exec is listed with type exec even when the config
// never declares an exec agent (consistency with P1's worker capability report).
func TestMetaAgentExecBuiltin(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"p": {HostPath: root, AllowExec: true},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: "cli-agent"}, // note: NO exec declared
		},
	}
	m := getMeta(t, wireMetaServer(t, cfg, nil))
	byKey := map[string]metaAgent{}
	for _, a := range m.Agents {
		byKey[a.Key] = a
	}
	if a, ok := byKey["exec"]; !ok || a.Type != "exec" {
		t.Fatalf("built-in exec must be listed with type exec even undeclared: %+v", m.Agents)
	}
}

// TestMetaWorkerAgentCaps (P4 T4.2): a connected worker's metaWorker.agent_caps
// carries the typed {key,type,interactive} detail; an offline worker carries none.
func TestMetaWorkerAgentCaps(t *testing.T) {
	workers := fakeWorkers{
		"w-online": {
			Connected:     true,
			LastHeartbeat: 1750300000000,
			Projects:      []string{"proj-a"},
			Agents:        []string{"exec", "claude"},
			AgentCaps: []AgentBrief{
				{Key: "exec", Type: "exec"},
				{Key: "claude", Type: "cli-agent", Interactive: true},
			},
		},
	}
	m := getMeta(t, newMetaServer(t, workers))
	byID := map[string]metaWorker{}
	for _, w := range m.Workers {
		byID[w.ID] = w
	}
	on := byID["w-online"]
	caps := map[string]AgentBrief{}
	for _, c := range on.AgentCaps {
		caps[c.Key] = c
	}
	if len(on.AgentCaps) != 2 {
		t.Fatalf("w-online should carry 2 agent_caps, got %+v", on.AgentCaps)
	}
	if caps["claude"].Type != "cli-agent" || !caps["claude"].Interactive {
		t.Fatalf("claude cap wrong: %+v", caps["claude"])
	}
	if caps["exec"].Type != "exec" || caps["exec"].Interactive {
		t.Fatalf("exec cap wrong: %+v", caps["exec"])
	}
	if off := byID["w-offline"]; len(off.AgentCaps) != 0 {
		t.Fatalf("offline worker must carry no agent_caps: %+v", off)
	}
}

// TestMetaWorkerOnlyProjects (federation follow-up): a project key reported ONLY
// by an online worker (no host cfg) surfaces as a synthesized metaProject with
// worker_only=true and empty allowlists; a project defined on BOTH host and worker
// stays a single host entry (worker_only false); an OFFLINE worker's project keys
// are not surfaced (connected-gated, same rule as agent_caps).
func TestMetaWorkerOnlyProjects(t *testing.T) {
	workers := fakeWorkers{
		"w-online": {
			Connected: true,
			Projects:  []string{"proj-a", "wonly"}, // proj-a=host, wonly=worker-only
			Agents:    []string{"codex"},
		},
		"w-offline": {
			Connected: false,
			Projects:  []string{"ghost"}, // offline → must NOT surface
		},
	}
	m := getMeta(t, newMetaServer(t, workers))

	byKey := map[string]metaProject{}
	for _, p := range m.Projects {
		if _, dup := byKey[p.Key]; dup {
			t.Fatalf("duplicate project key %q in meta.projects: %+v", p.Key, m.Projects)
		}
		byKey[p.Key] = p
	}

	// host project present, single entry, not marked, keeps its allowlists
	pa, ok := byKey["proj-a"]
	if !ok || pa.WorkerOnly {
		t.Fatalf("proj-a should be a single host entry with worker_only false: %+v", pa)
	}
	if len(pa.AllowedAgents) != 2 || len(pa.AllowedRunners) != 2 {
		t.Fatalf("proj-a host allowlists lost: %+v", pa)
	}

	// worker-only project surfaced with the marker + empty host config
	wo, ok := byKey["wonly"]
	if !ok {
		t.Fatalf("worker-only project 'wonly' missing from meta.projects: %+v", m.Projects)
	}
	if !wo.WorkerOnly {
		t.Fatalf("'wonly' must be marked worker_only: %+v", wo)
	}
	if len(wo.AllowedAgents) != 0 || len(wo.AllowedRunners) != 0 || wo.DefaultAgent != "" {
		t.Fatalf("'wonly' must carry empty host config: %+v", wo)
	}

	// offline worker's project must not surface
	if _, ok := byKey["ghost"]; ok {
		t.Fatalf("offline worker's project 'ghost' must not surface: %+v", m.Projects)
	}
}

// TestMetaWorkerOnlyDedup: the same worker-only project key reported by multiple
// online workers yields a single synthesized entry.
func TestMetaWorkerOnlyDedup(t *testing.T) {
	workers := fakeWorkers{
		"w-online":  {Connected: true, Projects: []string{"shared-wonly"}},
		"w-offline": {Connected: true, Projects: []string{"shared-wonly"}},
	}
	m := getMeta(t, newMetaServer(t, workers))
	count := 0
	for _, p := range m.Projects {
		if p.Key != "shared-wonly" {
			continue
		}
		count++
		if !p.WorkerOnly {
			t.Fatalf("shared-wonly must be worker_only: %+v", p)
		}
	}
	if count != 1 {
		t.Fatalf("shared worker-only key must dedup to 1 entry, got %d: %+v", count, m.Projects)
	}
}

// TestMetaRunnerWorkerID: a worker runner surfaces the worker it is PINNED to in
// config, so the form can narrow agents/projects by that worker's caps without the
// user re-picking it (the submit path already falls back to it — capabilitiesFor /
// selectTargetWorker). The implicit local runner carries no worker_id.
func TestMetaRunnerWorkerID(t *testing.T) {
	m := getMeta(t, newMetaServer(t, nil))
	for _, r := range m.Runners {
		switch r.Name {
		case "w":
			if r.WorkerID != "w-online" {
				t.Fatalf("worker runner must expose its pinned worker_id, got %q: %+v", r.WorkerID, r)
			}
		case "local":
			if r.WorkerID != "" {
				t.Fatalf("local runner must not carry a worker_id: %+v", r)
			}
		}
	}
}

// TestMetaProjectGates: interactive_allowed_agents and allow_exec are the two
// admission gates INDEPENDENT of allowed_agents (job/config.go). The form needs both
// or it lists agents that are guaranteed to be rejected at submit.
func TestMetaProjectGates(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"gated": {
				HostPath:                 root,
				AllowExec:                true,
				InteractiveAllowedAgents: []string{"tty"},
			},
			"plain": {HostPath: root}, // no exec, no interactive
		},
		Agents: map[string]config.AgentConfig{
			"tty":  {Type: "cli-agent", Interactive: true, NoRawCmd: true},
			"exec": {Type: "exec"},
		},
	}
	m := getMeta(t, wireMetaServer(t, cfg, nil))

	byKey := map[string]metaProject{}
	for _, p := range m.Projects {
		byKey[p.Key] = p
	}
	gated, plain := byKey["gated"], byKey["plain"]
	if !gated.AllowExec || len(gated.InteractiveAllowedAgents) != 1 || gated.InteractiveAllowedAgents[0] != "tty" {
		t.Fatalf("gated project must carry both gates: %+v", gated)
	}
	if plain.AllowExec {
		t.Fatalf("plain project must report allow_exec=false: %+v", plain)
	}
	// non-nil empty array, never JSON null (the form does a set-intersection on it)
	if plain.InteractiveAllowedAgents == nil || len(plain.InteractiveAllowedAgents) != 0 {
		t.Fatalf("empty interactive allowlist must serialise as []: %+v", plain)
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
		if w.Connected || len(w.Labels) != 0 || len(w.Projects) != 0 || len(w.Agents) != 0 {
			t.Fatalf("nil registry worker should be disconnected: %+v", w)
		}
	}
}
