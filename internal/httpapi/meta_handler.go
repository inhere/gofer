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
// agent / runner dropdowns once a project is picked (plan P3-b).
type metaProject struct {
	Key            string   `json:"key"`
	AllowedAgents  []string `json:"allowed_agents"`
	AllowedRunners []string `json:"allowed_runners"`
	DefaultAgent   string   `json:"default_agent,omitempty"`
}

// metaAgent is one selectable agent: its key plus type (cli-agent vs exec) which
// the form keys on to show a prompt textarea (cli-agent) or a command input (exec).
type metaAgent struct {
	Key  string `json:"key"`
	Type string `json:"type"`
}

// metaRunner is one selectable runner: name + type (local / peer-http / worker).
// runner=worker drives the optional worker_id / worker_labels picker.
type metaRunner struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// metaWorker is one registered worker the form may target explicitly: its id,
// advertised labels and live connection state. Connected/labels come from the
// same WorkerStatus snapshot /v1/runners uses (so the two views agree); a
// configured-but-disconnected worker reports connected=false with no labels.
type metaWorker struct {
	ID        string   `json:"id"`
	Labels    []string `json:"labels,omitempty"`
	Projects  []string `json:"projects,omitempty"`
	Agents    []string `json:"agents,omitempty"`
	Connected bool     `json:"connected"`
}

// handleMeta returns the aggregated form options (G4). Each group is always a
// non-nil array. projects/agents/runners come from the config snapshot (same
// sources as /v1/projects, /v1/agents, /v1/runners); workers are the configured
// worker ids resolved against the WorkerStatus snapshot for connected/labels.
func (s *Server) handleMeta(c *rux.Context) {
	c.JSON(http.StatusOK, metaResp{
		Projects: s.metaProjects(),
		Agents:   s.metaAgents(),
		Runners:  s.metaRunners(),
		Workers:  s.metaWorkers(),
	})
}

// metaProjects lists every project with its allowlists, sorted by key. Allowed
// lists are normalised to non-nil arrays so the front-end never sees JSON null.
func (s *Server) metaProjects() []metaProject {
	keys := s.projects.List() // already sorted (project.Registry.List)
	out := make([]metaProject, 0, len(keys))
	for _, k := range keys {
		p, err := s.projects.Get(k)
		if err != nil {
			continue
		}
		out = append(out, metaProject{
			Key:            k,
			AllowedAgents:  nonNil(p.AllowedAgents),
			AllowedRunners: nonNil(p.AllowedRunners),
			DefaultAgent:   p.DefaultAgent,
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
		out = append(out, metaAgent{Key: k, Type: list[k].Type})
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
		out = append(out, metaRunner{Name: name, Type: rc.Type})
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
