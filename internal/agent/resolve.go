package agent

import (
	"os/exec"

	"github.com/inhere/gofer/internal/config"
)

// Detector probes which agent CLIs are actually installed on THIS host. It is the
// injection seam that lets the real probe be swapped for a hermetic fake, which is
// what keeps `go test ./...` independent of whatever CLIs happen to sit on the
// developer's PATH (see core.WithAgentDetector / NoopDetector).
//
// Contract:
//   - Detect is called AT MOST ONCE per Resolve, and gets EVERY candidate agent in
//     one call — so an implementation is free to fan out in parallel and enforce a
//     single overall budget rather than a per-agent one.
//   - It must never panic and never block indefinitely. A failed probe is reported
//     as Available=false with an Error, never as a returned error.
//   - A key missing from the returned map is read as unavailable.
type Detector interface {
	Detect(agents map[string]config.AgentConfig) map[string]DetectResult
}

// Resolve is the SINGLE entry point for "detect the host's agent CLIs, then merge the
// built-in templates into a config". Every assembly path funnels through it —
// core.Build, core.ReloadWith, and the core-less registries in internal/commands — so
// `gofer agent list`, /v1/agents, the MCP tools and a worker's advertised caps can
// never disagree about which agents exist on a box.
//
// IRON RULE (P2): an ESCAPE-HATCH agent — one the operator declared in the config —
// is NEVER removed because a probe failed. Only TEMPLATE-INJECTED agents are
// detect-gated. A name clash is won by the escape hatch ENTIRELY: a template is
// injected only when its key does not exist, and no field-level merge is ever done
// (Interactive/NoRawCmd are plain bools, so "unset" and "explicit false" cannot be
// told apart and a partial merge would silently flip them).
//
// Resolve mutates cfg IN PLACE and returns it, so the one *config.Config pointer that
// the core, the registries and the project write-back path all share stays a single
// object. It runs before that pointer is published to any atomic snapshot, so it adds
// no concurrent write.
//
// Idempotent: keys injected by a previous Resolve are stripped first and re-gated from
// scratch, so resolve∘resolve == resolve. Without that, a second pass would read a
// previously injected template as an operator declaration and silently promote it to an
// un-gated escape hatch — i.e. the detect gate would only ever apply on the first pass.
//
// The second return carries the detect result for EVERY probed agent (templates and
// escape hatches alike): it is the one detect pass per config, and the source T5's
// availability cache is meant to be fed from. Callers that do not need it discard it.
func Resolve(cfg *config.Config, d Detector) (*config.Config, map[string]DetectResult) {
	if cfg == nil {
		return nil, nil
	}
	if d == nil {
		d = NoopDetector{}
	}

	// Undo the previous resolve (if any) so this pass re-gates from scratch.
	for key := range cfg.InjectedAgents() {
		delete(cfg.Agents, key)
	}
	cfg.MarkInjectedAgents(nil)

	// Probe the operator's agents AND the injectable templates in one call. The
	// escape hatches are probed for their availability report only — they are never
	// gated on the outcome.
	candidates := make(map[string]config.AgentConfig, len(cfg.Agents)+len(builtinTemplates)+1)
	for key, ac := range cfg.Agents {
		candidates[key] = ac
	}
	if _, declared := candidates[ExecAgentKey]; !declared {
		candidates[ExecAgentKey] = builtinExecAgent()
	}
	injectable := make(map[string]config.AgentConfig, len(builtinTemplates))
	for key, tpl := range builtinTemplates {
		if _, declared := cfg.Agents[key]; declared {
			continue // escape hatch wins the whole entry
		}
		injectable[key] = tpl
		candidates[key] = tpl
	}

	detected := d.Detect(candidates)

	injected := make(map[string]bool, len(injectable))
	for key, tpl := range injectable {
		if !detected[key].Available {
			continue // the template's CLI is not on this host: do not offer the agent
		}
		if cfg.Agents == nil {
			cfg.Agents = map[string]config.AgentConfig{}
		}
		cfg.Agents[key] = tpl
		injected[key] = true
	}
	cfg.MarkInjectedAgents(injected)
	return cfg, detected
}

// DefaultDetector returns the Detector every production assembly path uses when the
// caller injects none.
//
// T0 ships the minimal form (PATH lookup only). T2 replaces the body with a parallel,
// budgeted probe that additionally fills Version best-effort; the interface and the
// Available semantics below do not change.
func DefaultDetector() Detector { return lookPathDetector{} }

// lookPathDetector decides availability from a PATH lookup alone.
type lookPathDetector struct{}

// Detect implements Detector.
func (lookPathDetector) Detect(agents map[string]config.AgentConfig) map[string]DetectResult {
	out := make(map[string]DetectResult, len(agents))
	for key, ac := range agents {
		out[key] = lookPathProbe(ac)
	}
	return out
}

// lookPathProbe reports availability from PATH resolution and NEVER from a child
// process' exit code: a slow-starting CLI, a first-run wizard or an auth prompt would
// probe as a FALSE NEGATIVE, and a false negative means the agent disappears from the
// caps report and its jobs get rejected. Being on PATH is exactly the condition for
// being launchable (exec.Command and exec.LookPath share one lookup).
func lookPathProbe(ac config.AgentConfig) DetectResult {
	// exec runs the request's argv verbatim and needs no external CLI. This check
	// MUST come before anything that consults a command: a Windows host has no `sh`,
	// so probing exec would otherwise report the BUILT-IN exec agent unavailable.
	if ac.Type == TypeExec {
		return DetectResult{Available: true, Version: "builtin"}
	}
	if ac.Command == "" {
		return DetectResult{Error: "no command configured"}
	}
	if _, err := exec.LookPath(ac.Command); err != nil {
		return DetectResult{Error: err.Error()}
	}
	return DetectResult{Available: true}
}

// NoopDetector reports every agent unavailable without touching the host: no PATH
// lookup, no child process. Inject it wherever template materialization must not
// depend on what happens to be installed — every test that builds a Core, and any
// caller that must stay hermetic.
//
// With it, Resolve injects NOTHING; the operator's declared agents are untouched (the
// iron rule keeps escape hatches regardless of any probe outcome), so a config resolves
// to exactly the agent set it declared.
type NoopDetector struct{}

// Detect implements Detector.
func (NoopDetector) Detect(map[string]config.AgentConfig) map[string]DetectResult { return nil }
