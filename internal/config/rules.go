package config

import (
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// DefaultRulesMaxBytes is the shipped cap on the TOTAL rule text injected into one
// job (JOB-06① 决策 3): 16KiB. Rules are meant to be short and hard — a submit whose
// rules exceed the cap is rejected naming the largest ones, and long reference
// material belongs in a skill (which the agent reads only if it needs it).
const DefaultRulesMaxBytes = 16384

// EffectiveRules resolves the MANDATORY rule set bound to one job (JOB-06①, design
// §一.2): the union of the four binding levels, in the order they are accumulated —
//
//	server.rules                      → the deployment's discipline
//	agents.<agentKey>.rules           → this agent's own constraints
//	projects.<projectKey>.rules       → this repository's conventions
//	the request's --rule names        → this one job
//
// — deduplicated keeping FIRST-appearance order. This is the same function shape as
// EffectiveSkills (and for the same reason): a rule is accumulated, not a
// mutually-exclusive policy, so a nearer layer must not be able to unhook a broader
// one. The repository's own `.gofer/RULES.md` is NOT resolved here: it is a file the
// job service reads from the project checkout and appends AFTER these bindings as
// `project:<key>` (it needs the project's exec path, which this package does not
// resolve).
//
// The two off switches mirror EffectiveSkills:
//
//	disable (the request's --no-rules) → nil: an explicit "this job gets no rules"
//	  wins over every configured level. Only a user caller may set it (design 决策 4):
//	  the HTTP layer refuses a job credential that tries.
//	agentType == "exec"                → nil: an exec agent runs a caller-supplied
//	  command and has no prompt to inject into, so binding rules to it is meaningless.
//
// A projectKey or agentKey the config does not define is normal, not an error (the
// worker side of a POLICY-pushed config, or the built-in local runner): the levels
// that DO exist still apply. Blank/whitespace-only names are dropped, and the result
// is nil when nothing at all is configured, without allocating — this runs on every
// submit and the overwhelmingly common answer is "no rules".
func (c *Config) EffectiveRules(projectKey, agentKey string, requested []string, disable bool, agentType string) []string {
	if disable || agentType == execAgentType {
		return nil
	}
	var server, agent, project []string
	if c != nil {
		server = c.Server.Rules
		if a, ok := c.Agents[agentKey]; ok {
			agent = a.Rules
		}
		if p, ok := c.Projects[projectKey]; ok {
			project = p.Rules
		}
	}
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
	if len(out) == 0 {
		return nil
	}
	return out
}

// EffectiveRulesMaxBytes is the total-rule-text cap for one job
// (server.rules_max_bytes), with DefaultRulesMaxBytes as the shipped default. 0 (or
// an absent block) reads as "unset" — the way every other cap in gofer's config
// behaves — and a negative value is treated as unset too rather than as "reject
// everything". Read per submit, so a hot edit applies to the NEXT job.
func (c *Config) EffectiveRulesMaxBytes() int {
	if c != nil && c.Server.RulesMaxBytes > 0 {
		return c.Server.RulesMaxBytes
	}
	return DefaultRulesMaxBytes
}
