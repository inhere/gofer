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
// is not allowed.
func CheckAllowed(cfg allowedAgentsLookup, projectKey, agentKey string) error {
	allowed, ok := cfg.ProjectAllowedAgents(projectKey)
	if !ok {
		return fmt.Errorf("unknown project %q", projectKey)
	}
	for _, a := range allowed {
		if a == agentKey {
			return nil
		}
	}
	return fmt.Errorf("agent %q is not allowed in project %q", agentKey, projectKey)
}
