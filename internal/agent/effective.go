package agent

import "github.com/inhere/gofer/internal/config"

// EffectiveAgents returns every agent cfg declares, plus the built-in exec agent, as
// it actually runs: session and output defaults applied, the probe that availability
// detection would use filled in, and `extends` cleared (how an agent was written, not
// what it runs as). Two configs whose effective agents are equal behave the same, so
// `gofer config lint` and the reload receipt compare this instead of the yaml text.
func EffectiveAgents(cfg *config.Config) map[string]config.AgentConfig {
	keys := map[string]bool{ExecAgentKey: true}
	if cfg != nil {
		for k := range cfg.Agents {
			keys[k] = true
		}
	}
	out := make(map[string]config.AgentConfig, len(keys))
	for k := range keys {
		a, ok := ResolveAgent(cfg, k)
		if !ok {
			continue
		}
		a.Extends = ""
		switch {
		case a.Type == TypeExec:
			a.Detect = config.DetectConfig{} // exec is never probed
		case a.Detect.Command == "":
			a.Detect = config.DetectConfig{Command: a.Command, Args: []string{"--version"}}
		}
		out[k] = a
	}
	return out
}
