package agent

import "fmt"

// allowedAgentsLookup is the minimal projects view CheckAllowed needs.
// *config.Config satisfies it via ProjectAllowedAgents; keeping it an interface
// avoids an import cycle and lets tests pass a stub.
type allowedAgentsLookup interface {
	ProjectAllowedAgents(projectKey string) ([]string, bool)
}

// CheckAllowed verifies that agentKey is listed in the project's allowed_agents.
// The built-in exec agent is NOT exempt: a project must list "exec" in its
// allowed_agents for exec to be usable (plan §11 — built-in does not bypass the
// project allowlist). Returns an error when the project is unknown or the agent
// is not allowed. A project without an allowed_agents list admits every agent (the
// exec agent is still gated by allow_exec, which callers check separately).
func CheckAllowed(cfg allowedAgentsLookup, projectKey, agentKey string) error {
	allowed, ok := cfg.ProjectAllowedAgents(projectKey)
	if !ok {
		return fmt.Errorf("unknown project %q", projectKey)
	}
	// An EMPTY allowed_agents list admits every configured agent (config-optimize §13):
	// job submit only enforces a declared list, and every other gate (agent probe,
	// fallback candidates, comment mentions) must agree with it.
	if len(allowed) == 0 {
		return nil
	}
	for _, a := range allowed {
		if a == agentKey {
			return nil
		}
	}
	return fmt.Errorf("agent %q is not allowed in project %q", agentKey, projectKey)
}
