package httpapi

import (
	"net/http"
	"sort"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
)

// metaResp is the read-only form-options aggregate for the web console submit
// form (G4 / design §6.4). It bundles everything the NewJob form needs in one
// authed GET so the front-end does not fan out across /v1/projects, /v1/agents
// and /v1/runners. All data is read from the config snapshot + the same worker
// connected-state source as /v1/runners (WorkerStatus); it never queries the DB.
type metaResp struct {
	Projects []metaProject `json:"projects"`
	Agents   []metaAgent   `json:"agents"`
	Runners  []metaRunner  `json:"runners"`
	Workers  []metaWorker  `json:"workers"`
}

// metaProject carries the per-project allowlists the form uses to constrain the
// agent / runner dropdowns once a project is picked (plan P3-b). WorkerOnly marks
// a project that has NO host config and is defined only on an online worker
// (federation follow-up): it is surfaced so a worker-runner submit can target it,
// but consumers that cannot run it locally (workflows, baseline/local pickers)
// filter it out by this flag. Worker-only entries carry empty allowlists.
//
// InteractiveAllowedAgents / AllowExec mirror the two admission gates that are
// INDEPENDENT of AllowedAgents (job/config.go): an interactive job needs its agent
// listed in interactive_allowed_agents (empty = no interactive job at all), and an
// exec-type agent needs allow_exec on a LOCAL runner (worker/peer enforce their
// own). Without them the form cannot tell a selectable agent from one that is
// guaranteed to be rejected at submit.
//
// AllowExec is emitted UNCONDITIONALLY (no omitempty): the web console is served
// from disk (--web-dir) while the binary ships separately, so a new console can run
// against an older server. The console distinguishes "gate says false" from "this
// server predates the field" by presence — omitting false would collapse the two and
// make the old server look like allow_exec=false (hiding every exec agent).
type metaProject struct {
	Key                      string   `json:"key"`
	AllowedAgents            []string `json:"allowed_agents"`
	AllowedRunners           []string `json:"allowed_runners"`
	InteractiveAllowedAgents []string `json:"interactive_allowed_agents"`
	AllowExec                bool     `json:"allow_exec"`
	DefaultAgent             string   `json:"default_agent,omitempty"`
	WorkerOnly               bool     `json:"worker_only,omitempty"`
}

// metaAgent is one selectable agent: its key, type (cli-agent vs exec) which the
// form keys on to show a prompt textarea (cli-agent) or a command input (exec), and
// the interactive flag (P4) so the cascade can filter interactive agents. Type +
// interactive come from the RESOLVED agent registry (built-in exec included with
// type exec), not the raw cfg.Agents map (consistency with P1's worker report).
type metaAgent struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Interactive bool   `json:"interactive,omitempty"`
}

// metaRunner is one selectable runner: name + type (local / peer-http / worker).
// runner=worker drives the optional worker_id / worker_labels picker.
//
// WorkerID (type=worker only) is the worker this runner is PINNED to in config.
// The submit path already resolves it — an empty request worker_id falls back to
// it for both the capability gate (job.capabilitiesFor) and dispatch (D4) — so the
// form must see it too: without it the cascade cannot narrow agents/projects until
// the user redundantly re-picks the very worker the runner already names.
type metaRunner struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	WorkerID string `json:"worker_id,omitempty"`
}

// metaWorker is one registered worker the form may target explicitly: its id,
// advertised labels and live connection state. Connected/labels come from the
// same WorkerStatus snapshot /v1/runners uses (so the two views agree); a
// configured-but-disconnected worker reports connected=false with no labels.
type metaWorker struct {
	ID       string   `json:"id"`
	Labels   []string `json:"labels,omitempty"`
	Projects []string `json:"projects,omitempty"`
	// Agents stays the bare-key list (back-compat); AgentCaps carries the typed
	// detail (key/type/interactive) the P4 cascade narrows the agent dropdown by.
	Agents    []string     `json:"agents,omitempty"`
	AgentCaps []AgentBrief `json:"agent_caps,omitempty"`
	Connected bool         `json:"connected"`
}

// handleMeta returns the aggregated form options (G4). Each group is always a
// non-nil array. projects/agents/runners come from the config snapshot (same
// sources as /v1/projects, /v1/agents, /v1/runners); workers are the configured
// worker ids resolved against the WorkerStatus snapshot for connected/labels.
func (s *Server) handleMeta(c *rux.Context) {
	workers := s.metaWorkers()
	c.JSON(http.StatusOK, metaResp{
		Projects: s.metaProjects(workers),
		Agents:   s.metaAgents(),
		Runners:  s.metaRunners(),
		Workers:  workers,
	})
}

// metaProjects lists every project with its allowlists, sorted by key. Allowed
// lists are normalised to non-nil arrays so the front-end never sees JSON null.
//
// After the host projects it appends worker-only projects: keys reported by an
// ONLINE worker (from the already-built workers list, connected-gated like
// agent_caps) that the host has no config for. Each is synthesized once (dedup
// across workers) with WorkerOnly=true and empty allowlists — the host cannot run
// it locally; only a submit targeting that worker can. A project defined on BOTH
// host and worker stays the single host entry (WorkerOnly=false, host wins).
// Order is deterministic: host projects (Registry.List order) then worker-only
// keys sorted.
func (s *Server) metaProjects(workers []metaWorker) []metaProject {
	keys := s.projects.List() // already sorted (project.Registry.List)
	out := make([]metaProject, 0, len(keys))
	host := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		p, err := s.projects.Get(k)
		if err != nil {
			continue
		}
		host[k] = struct{}{}
		out = append(out, metaProject{
			Key:                      k,
			AllowedAgents:            nonNil(p.AllowedAgents),
			AllowedRunners:           nonNil(p.AllowedRunners),
			InteractiveAllowedAgents: nonNil(p.InteractiveAllowedAgents),
			AllowExec:                p.AllowExec,
			DefaultAgent:             p.DefaultAgent,
		})
	}
	// Union of online workers' reported project keys not already host-defined.
	// workers[].Projects is only populated for connected workers (metaWorkers gate).
	seen := make(map[string]struct{})
	extra := make([]string, 0)
	for _, w := range workers {
		if !w.Connected {
			continue
		}
		for _, pk := range w.Projects {
			if pk == "" {
				continue
			}
			if _, ok := host[pk]; ok {
				continue // defined on host too → single host entry wins
			}
			if _, ok := seen[pk]; ok {
				continue // dedup across workers
			}
			seen[pk] = struct{}{}
			extra = append(extra, pk)
		}
	}
	sort.Strings(extra)
	// Worker-only entries carry empty gates: the host never runs them, so its
	// allowlists / allow_exec / interactive_allowed_agents do not apply — the worker
	// validates with its own config on dispatch. (Interactive IS still host-gated and
	// therefore unavailable for a worker-only project; see job/config.go's
	// workerOnlyProject, which synthesizes the same empty interactive allowlist.)
	for _, pk := range extra {
		out = append(out, metaProject{
			Key:                      pk,
			AllowedAgents:            nonNil(nil),
			AllowedRunners:           nonNil(nil),
			InteractiveAllowedAgents: nonNil(nil),
			WorkerOnly:               true,
		})
	}
	return out
}

// metaAgents lists every configured agent (key+type), sorted by key. It does NOT
// run the availability probe (that is /v1/agents' job); the form only needs the
// type to pick the prompt-vs-command input.
func (s *Server) metaAgents() []metaAgent {
	list := s.agents.List()
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]metaAgent, 0, len(keys))
	for _, k := range keys {
		out = append(out, metaAgent{Key: k, Type: list[k].Type, Interactive: list[k].Interactive})
	}
	return out
}

// metaRunners lists the implicit local runner plus every configured runner
// (name+type), local first then by name — mirroring /v1/runners' ordering.
func (s *Server) metaRunners() []metaRunner {
	out := make([]metaRunner, 0, len(s.runners)+1)
	out = append(out, metaRunner{Name: runnerTypeLocal, Type: runnerTypeLocal})
	for name, rc := range s.runners {
		if rc.Type == runnerTypeLocal || name == runnerTypeLocal {
			continue // would collide with the implicit local row
		}
		mr := metaRunner{Name: name, Type: rc.Type}
		if rc.Type == runnerTypeWorker {
			mr.WorkerID = rc.WorkerID // config-pinned target (may be empty → labels-only runner)
		}
		out = append(out, mr)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type == runnerTypeLocal && out[j].Type != runnerTypeLocal {
			return true
		}
		if out[j].Type == runnerTypeLocal && out[i].Type != runnerTypeLocal {
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// metaWorkers lists every registered worker (cfg.Server.Workers) with its live
// connection state and labels, sorted by id. Connected/labels reuse the same
// WorkerStatus source as /v1/runners: a nil registry (not wired) or an
// offline/never-connected worker yields connected=false with no labels.
func (s *Server) metaWorkers() []metaWorker {
	ids := make([]string, 0, len(s.workerConfigs()))
	for id := range s.workerConfigs() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]metaWorker, 0, len(ids))
	for _, id := range ids {
		mw := metaWorker{ID: id}
		if s.workers != nil {
			if ws, ok := s.workers.WorkerStatus(id); ok && ws.Connected {
				mw.Connected = true
				mw.Labels = ws.Labels
				mw.Projects = ws.Projects
				mw.Agents = ws.Agents
				mw.AgentCaps = ws.AgentCaps
			}
		}
		out = append(out, mw)
	}
	return out
}

// workerConfigs returns the configured worker auth set (the registered worker
// ids), nil-safe for a Server built without a ServerConfig.
func (s *Server) workerConfigs() map[string]config.WorkerAuthConfig {
	if s.cfg == nil {
		return nil
	}
	return s.cfg.Workers
}

// nonNil returns a non-nil slice so empty allowlists serialise as [] not null.
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
