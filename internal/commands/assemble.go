package commands

import (
	"fmt"
	"os"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/job"
	"dev-agent-bridge/internal/jobstore"
	"dev-agent-bridge/internal/project"
	"dev-agent-bridge/internal/runner"
	localrunner "dev-agent-bridge/internal/runner/local"
	peerhttprunner "dev-agent-bridge/internal/runner/peerhttp"
)

// Core bundles the runtime objects assembled from a loaded config: the
// project/agent registries, the runner set, the SQLite metadata store and the
// job service. Both `serve` (HTTP control plane) and `mcp` (stdio MCP server)
// wire the same Core so the MCP tools reuse the identical job.Service (plan §P8:
// "MCP 内部复用 job.Service", 不复制执行逻辑).
type Core struct {
	Cfg      *config.Config
	Projects *project.Registry
	Agents   *agent.Registry
	Runners  map[string]runner.Runner
	Store    *jobstore.Store
	Jobs     *job.Service
}

// Close releases the Core's owned resources — currently the SQLite metadata
// store. Callers (serve/mcp) defer it for graceful shutdown so WAL is
// checkpointed and the db handle closed cleanly (design §14).
func (c *Core) Close() error {
	if c == nil || c.Store == nil {
		return nil
	}
	return c.Store.Close()
}

// buildCore assembles the registries, runner set, metadata store and job service
// from cfg. It is the single wiring point shared by serve and mcp; peer-http
// runners declared in config are registered too (plan §11.1, P7). It opens the
// SQLite metadata db (design §11 ResolveDBPath) and returns an error if that
// fails, since the job service cannot operate without it.
func buildCore(cfg *config.Config) (*Core, error) {
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	for name, rc := range cfg.Runners {
		if rc.Type == "peer-http" {
			token := ""
			if rc.TokenEnv != "" {
				token = os.Getenv(rc.TokenEnv)
			}
			runners[name] = peerhttprunner.New(name, rc.BaseURL, token)
		}
	}
	store, err := jobstore.Open(cfg.ResolveDBPath())
	if err != nil {
		return nil, fmt.Errorf("open metadata store: %w", err)
	}
	jobs := job.NewService(cfg, projects, agents, runners, store)
	return &Core{Cfg: cfg, Projects: projects, Agents: agents, Runners: runners, Store: store, Jobs: jobs}, nil
}
