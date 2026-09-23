package config

import (
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// execAgentType is the AgentConfig.Type whose jobs run a caller-supplied command
// instead of an agent CLI (see EffectiveStallTimeoutSec, which reads it for the same
// reason). It is spelled here rather than imported from internal/agent because that
// package imports this one.
const execAgentType = "exec"

// EffectiveSkills resolves the skill set bound to one job (JOB-10, design §一.3):
// the union of the four binding levels, in the order they are accumulated —
//
//	server.skills                     → the deployment's house rules
//	agents.<agentKey>.skills          → this agent's own quirks
//	projects.<projectKey>.skills      → this repository's conventions
//	the request's --skill names       → this one job
//
// — deduplicated keeping FIRST-appearance order. The order is what the mounted skill
// list, the prompt's skill section and `job show`'s `skills:` line all present, and
// it puts the broadest-scope knowledge first: a reader (human or agent) meets the
// house rules before the job's own.
//
// It is a UNION, deliberately unlike the nearest-layer-wins resolvers next to it
// (EffectiveRetryPolicy, AgentFallbacksFor, EffectiveApproval). Those resolve a
// mutually exclusive POLICY, where a nearer layer must be able to say "not this";
// skills are additive knowledge, so an agent adding its own skill must not silently
// unhook the project's. The single off switches are the two below.
//
// Two of the five arguments are off switches, and both return nil rather than an
// empty slot in the list:
//
//	disable (the request's --no-skills) → nil: an explicit "this job reads nothing"
//	  must win over every configured level, including the request's own --skill names.
//	agentType == "exec"                 → nil: an exec agent runs a command; it has
//	  no notion of reading a document, so binding skills to it is meaningless.
//
// agentType is passed in rather than looked up here because the caller resolves the
// agent (role/template/alias) through agent.ResolveAgent, which imports this package.
//
// A projectKey or agentKey the config does not define is normal, not an error: it
// is the worker side of a POLICY-pushed config, or simply the built-in local runner
// — the levels that DO exist still apply. Blank/whitespace-only names are dropped
// (a yaml list of empty strings means "nothing", not a skill called ""), and the
// result is nil when nothing at all is configured, without allocating: this is
// called on every submit and the overwhelmingly common answer is "no skills".
func (c *Config) EffectiveSkills(projectKey, agentKey string, requested []string, disable bool, agentType string) []string {
	if disable || agentType == execAgentType {
		return nil
	}
	var server, agent, project []string
	if c != nil {
		server = c.Server.Skills
		if a, ok := c.Agents[agentKey]; ok {
			agent = a.Skills
		}
		if p, ok := c.Projects[projectKey]; ok {
			project = p.Skills
		}
	}
	// Size the accumulator from the four lists up front: it makes the empty case
	// (every level silent) an early nil return, and the union bounded by the sum of
	// the inputs can never need more room than that.
	n := util.CapSum(len(server), len(agent), len(project), len(requested))
	if n == 0 {
		return nil
	}
	out := make([]string, 0, n)
	seen := make(map[string]struct{}, n)
	for _, level := range [4][]string{server, agent, project, requested} {
		for _, name := range level {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	// Every name was blank: report "no skills" instead of an allocated empty list,
	// so callers and the JSON/yaml encoders see one shape for the empty answer.
	if len(out) == 0 {
		return nil
	}
	return out
}
