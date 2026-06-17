package commands

import (
	"os"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/job"
	"dev-agent-bridge/internal/project"
	"dev-agent-bridge/internal/runner"
	localrunner "dev-agent-bridge/internal/runner/local"
	peerhttprunner "dev-agent-bridge/internal/runner/peerhttp"
)

// Core bundles the runtime objects assembled from a loaded config: the
// project/agent registries, the runner set and the job service. Both `serve`
// (HTTP control plane) and `mcp` (stdio MCP server) wire the same Core so the
// MCP tools reuse the identical job.Service (plan §P8: "MCP 内部复用 job.Service",
// 不复制执行逻辑).
type Core struct {
	Cfg      *config.Config
	Projects *project.Registry
	Agents   *agent.Registry
	Runners  map[string]runner.Runner
	Jobs     *job.Service
}

// buildCore assembles the registries, runner set and job service from cfg. It is
// the single wiring point shared by serve and mcp; peer-http runners declared in
// config are registered too (plan §11.1, P7).
func buildCore(cfg *config.Config) *Core {
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
	jobs := job.NewService(cfg, projects, agents, runners)
	return &Core{Cfg: cfg, Projects: projects, Agents: agents, Runners: runners, Jobs: jobs}
}
