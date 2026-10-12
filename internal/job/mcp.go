package job

import (
	"fmt"
	"os"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

// resolveMCP expands the job's MCP bindings (MCP-06) in place: req.MCP becomes the
// final name list and req.MCPSpecs their declarations from cfg. A dispatched job
// (MCPResolved) already carries both and is left alone.
//
// Refused at submit: a name the registry does not declare, and — for a job this
// machine runs — an agent that has no way to take MCP servers (a cli-agent without
// mcp_format). A job for a remote runner is checked by the machine that runs it,
// whose agent definitions may differ.
func resolveMCP(cfg *config.Config, req *JobRequest, remote bool) error {
	if req.MCPResolved {
		// A released hold carries the decided names but not the declarations (internal,
		// never persisted): read them from this machine's registry again.
		if len(req.MCP) > 0 && req.MCPSpecs == nil {
			req.MCPSpecs = map[string]config.MCPServerConfig{}
			for _, name := range req.MCP {
				spec, ok := cfg.MCPServers[name]
				if !ok {
					return fmt.Errorf("%w: unknown MCP server %q (declare it under mcp_servers)", ErrInvalidRequest, name)
				}
				req.MCPSpecs[name] = spec
			}
		}
		return nil
	}
	// A continuation runs on the exec carrier but injects for the agent it resumes.
	agentKey := req.Agent
	if req.ResumeSourceAgent != "" {
		agentKey = req.ResumeSourceAgent
	}
	ac, acOK := agent.ResolveAgent(cfg, agentKey)
	agentType := ""
	if acOK {
		agentType = ac.Type
	}
	req.MCP = cfg.EffectiveMCP(req.ProjectKey, agentKey, req.MCP, req.NoMCP, agentType)
	req.MCPSpecs = nil
	if len(req.MCP) == 0 {
		return nil
	}
	specs := make(map[string]config.MCPServerConfig, len(req.MCP))
	for _, name := range req.MCP {
		spec, ok := cfg.MCPServers[name]
		if !ok {
			return fmt.Errorf("%w: unknown MCP server %q (declare it under mcp_servers)", ErrInvalidRequest, name)
		}
		specs[name] = spec
	}
	req.MCPSpecs = specs
	if remote || !acOK {
		return nil
	}
	switch format := MCPFormatOf(ac); {
	case format == "":
		return fmt.Errorf("%w: agent %q cannot take MCP servers (set mcp_format on it, or submit with --no-mcp)", ErrInvalidRequest, agentKey)
	case format == config.MCPFormatClaudeJSON && !hasMCPConfigFileArg(ac.MCPArgs):
		return fmt.Errorf("%w: agent %q has mcp_format %s but its mcp_args do not carry {{mcp_config_file}}", ErrInvalidRequest, agentKey, format)
	}
	return nil
}

// MCPFormatOf is how MCP servers reach agent ac: an acp-agent always takes them over
// the protocol; any other agent uses its mcp_format ("" = it cannot).
func MCPFormatOf(ac config.AgentConfig) string {
	if ac.Type == agent.TypeACPAgent {
		return config.MCPFormatACP
	}
	if ac.Type == agent.TypeExec {
		return ""
	}
	return ac.MCPFormat
}

// EventJobMCPInjected records which registry MCP servers a job's agent was given and
// any limitation worth knowing (MCP-06).
const EventJobMCPInjected = "job.mcp_injected"

// prepareMCP builds the job's MCP injection on the machine that runs it: the
// injection itself and the argv fragment to splice (codex -c overrides and/or the
// agent's mcp_args). A secret variable missing here, or an agent that cannot take
// MCP servers, refuses the job.
func (s *Service) prepareMCP(cfg *config.Config, req JobRequest, resultDir string) (mcpInjection, []string, error) {
	if len(req.MCP) == 0 {
		return mcpInjection{}, nil, nil
	}
	agentKey := req.Agent
	if req.ResumeSourceAgent != "" {
		agentKey = req.ResumeSourceAgent
	}
	ac, ok := agent.ResolveAgent(cfg, agentKey)
	if !ok {
		return mcpInjection{}, nil, fmt.Errorf("%w: agent %q not found for MCP injection", ErrInvalidRequest, agentKey)
	}
	format := MCPFormatOf(ac)
	if format == "" {
		return mcpInjection{}, nil, fmt.Errorf("%w: agent %q cannot take MCP servers (set mcp_format on it, or submit with --no-mcp)", ErrInvalidRequest, agentKey)
	}
	inj, err := buildMCPInjection(format, req.MCP, req.MCPSpecs, resultDir, os.LookupEnv)
	if err != nil {
		return mcpInjection{}, nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	fragment := append([]string(nil), inj.Args...)
	if inj.UseMCPArgs {
		if len(ac.MCPArgs) == 0 {
			return mcpInjection{}, nil, fmt.Errorf("%w: agent %q has mcp_format %s but no mcp_args (with {{mcp_config_file}})", ErrInvalidRequest, agentKey, format)
		}
		fragment = append(fragment, ac.MCPArgs...)
	}
	return inj, fragment, nil
}

// mergeACPMCP adds the registry servers to the agent's own acp.mcp_servers; an entry
// the agent definition already names wins (design §3.2).
func mergeACPMCP(own, registry []runner.ACPMCPServer) []runner.ACPMCPServer {
	if len(registry) == 0 {
		return own
	}
	have := map[string]bool{}
	for _, s := range own {
		have[s.Name] = true
	}
	out := append([]runner.ACPMCPServer(nil), own...)
	for _, s := range registry {
		if !have[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

func hasMCPConfigFileArg(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "{{mcp_config_file}}") {
			return true
		}
	}
	return false
}

// mcpWireSpecs renders the bound servers for a dispatch, in binding order.
func mcpWireSpecs(names []string, specs map[string]config.MCPServerConfig) []wsproto.MCPSpec {
	if len(names) == 0 {
		return nil
	}
	out := make([]wsproto.MCPSpec, 0, len(names))
	for _, n := range names {
		s, ok := specs[n]
		if !ok {
			continue
		}
		out = append(out, wsproto.MCPSpec{
			Name: n, Type: s.Type, Command: s.Command, Args: s.Args, Env: s.Env,
			URL: s.URL, Headers: s.Headers, SecretEnv: s.SecretEnv, SecretHeaders: s.SecretHeaders, Tools: s.Tools,
		})
	}
	return out
}

// MCPNamesFromWire is the binding order of a dispatch's MCP list.
func MCPNamesFromWire(in []wsproto.MCPSpec) []string {
	if len(in) == 0 {
		return nil
	}
	names := make([]string, 0, len(in))
	for _, s := range in {
		names = append(names, s.Name)
	}
	return names
}

// MCPSpecsFromWire turns a dispatch's MCP list back into declarations by name.
func MCPSpecsFromWire(in []wsproto.MCPSpec) map[string]config.MCPServerConfig {
	if len(in) == 0 {
		return nil
	}
	specs := make(map[string]config.MCPServerConfig, len(in))
	for _, s := range in {
		specs[s.Name] = config.MCPServerConfig{
			Type: s.Type, Command: s.Command, Args: s.Args, Env: s.Env,
			URL: s.URL, Headers: s.Headers, SecretEnv: s.SecretEnv, SecretHeaders: s.SecretHeaders, Tools: s.Tools,
		}
	}
	return specs
}
