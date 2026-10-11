package job

import (
	"fmt"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
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
	if format := MCPFormatOf(ac); format == "" {
		return fmt.Errorf("%w: agent %q cannot take MCP servers (set mcp_format on it, or submit with --no-mcp)", ErrInvalidRequest, agentKey)
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
