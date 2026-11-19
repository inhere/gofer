package agent

import "github.com/inhere/gofer/internal/config"

// InteractiveResumeArgv renders the command a PERSON types to continue a CLI agent session in
// their own terminal: the agent command plus its interactive resume template. It is a pure
// rendering: no GlobalArgs (those are options a gofer-run job needs, such as a sandbox
// override, and must not leak into a command a person runs by hand) and no takeover
// preconditions. nil when the agent is not a cli-agent or has no interactive resume template.
func InteractiveResumeArgv(ac config.AgentConfig, sessionID string) []string {
	if ac.Type != TypeCLIAgent || len(ac.SessionResumeInteractive) == 0 || ac.Command == "" {
		return nil
	}
	return append([]string{ac.Command}, Render(ac.SessionResumeInteractive, Vars{SessionID: sessionID})...)
}

// InteractiveNewArgv is the command that starts a fresh session of a CLI agent by hand: the
// agent command alone (again without GlobalArgs). nil for an agent that is not a cli-agent.
func InteractiveNewArgv(ac config.AgentConfig) []string {
	if ac.Type != TypeCLIAgent || ac.Command == "" {
		return nil
	}
	return []string{ac.Command}
}
