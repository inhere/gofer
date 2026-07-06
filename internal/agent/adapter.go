package agent

import "fmt"

// Resolved is the executable form of one job request: the command to run, its
// argv (excluding command), and the agent-config-level env. The runner (P4)
// layers process env on top of Env; the adapter returns only the agent's own
// env so callers control the merge order.
type Resolved struct {
	Command string
	Args    []string
	Env     map[string]string
}

// BuildOptions adjusts request resolution for paths with different admission
// semantics. The default zero value preserves the public Build behaviour.
type BuildOptions struct {
	AllowEmptyPrompt bool
}

// Build turns a single job request into an executable Resolved form. The
// argv-preserving contract of Render (plan §11) is upheld: nothing is ever
// joined into a shell string.
//
// Parameters (P4's JobRequest is not built yet, so adapter takes plain inputs):
//   - agentKey: which configured/built-in agent to use.
//   - prompt:   the prompt text for cli-agent agents.
//   - cmd:      the request-supplied argv for exec agents (argv[0] is the
//     command). Ignored for cli-agent agents.
//   - vars:     placeholder values for cli-agent template rendering. Prompt is
//     taken from the prompt parameter (vars.Prompt is overwritten to keep them
//     consistent).
func (r *Registry) Build(agentKey, prompt string, cmd []string, vars Vars) (Resolved, error) {
	return r.BuildWithOptions(agentKey, prompt, cmd, vars, BuildOptions{})
}

// BuildWithOptions is Build with explicit resolution options.
func (r *Registry) BuildWithOptions(agentKey, prompt string, cmd []string, vars Vars, opts BuildOptions) (Resolved, error) {
	ac, ok := r.Get(agentKey)
	if !ok {
		return Resolved{}, fmt.Errorf("unknown agent %q", agentKey)
	}

	switch ac.Type {
	case TypeExec:
		// exec ignores prompt/template; argv comes solely from the request cmd.
		if len(cmd) == 0 {
			return Resolved{}, fmt.Errorf("agent %q (exec) requires a command (cmd argv is empty)", agentKey)
		}
		return Resolved{
			Command: cmd[0],
			Args:    append([]string(nil), cmd[1:]...),
			Env:     copyEnv(ac.Env),
		}, nil

	case TypeCLIAgent:
		if prompt == "" && !opts.AllowEmptyPrompt {
			return Resolved{}, fmt.Errorf("agent %q (cli-agent) requires a non-empty prompt", agentKey)
		}
		if ac.Command == "" {
			return Resolved{}, fmt.Errorf("agent %q (cli-agent) has no command configured", agentKey)
		}
		vars.Prompt = prompt
		return Resolved{
			Command: ac.Command,
			Args:    Render(ac.Args, vars),
			Env:     copyEnv(ac.Env),
		}, nil

	default:
		return Resolved{}, fmt.Errorf("agent %q has unsupported type %q", agentKey, ac.Type)
	}
}

// copyEnv returns a shallow copy of m (nil-safe) so callers can mutate the
// merged env without affecting the loaded config.
func copyEnv(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
