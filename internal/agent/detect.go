package agent

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// Probe budgets. Detect sits on two hot paths — a worker's pre-register caps
// build and EVERY reload, which is answered under a 10s HTTP budget
// (httpapi/worker_reload_handler.go). A serial walk of N agents at 5s each blew
// that budget outright, so a whole batch now fans out in parallel under one
// overall ceiling and a single probe is bounded far tighter.
const (
	// detectTimeout bounds a SINGLE version probe (one child process).
	detectTimeout = 2 * time.Second
	// detectBudget is the hard ceiling for one whole detect batch, no matter how
	// many agents it carries.
	detectBudget = 3 * time.Second
)

// DetectResult reports an agent's availability.
//
// Available is decided by lookPathProbe and by NOTHING else: it never depends on
// a child process' exit code. Version is best-effort — a failed or hung version
// probe costs the version string and NEVER the availability.
//
// Error carries the reason an agent is unavailable (unknown key / no command /
// not on PATH). It is left empty when Available is true: consumers must read
// Available, not the presence of an error.
type DetectResult struct {
	Available bool
	Version   string
	Error     string
}

// Detect reports one agent's availability, for DISPLAY paths (`gofer agent
// detect`, GET /v1/agents, the MCP ListAgents tool). It shares the availability
// rule with the batch Detector (lookPathProbe) so a box can never report an
// agent as usable in one view and unusable in another.
//
// Semantics:
//   - Unknown agent -> Available=false with an error.
//   - exec agent -> Available=true, always (no external CLI to probe).
//   - Any other agent -> Available=true iff its command resolves on PATH. A
//     missing detect block is NOT a reason to report unavailable: it used to be,
//     which reported an installed CLI as unusable purely because the operator had
//     written no detect stanza.
//   - Version -> best-effort, bounded; failure leaves it empty, never flips
//     Available. Never panics.
func (r *Registry) Detect(agentKey string) DetectResult {
	ac, ok := r.Get(agentKey)
	if !ok {
		return DetectResult{Available: false, Error: "unknown agent " + agentKey}
	}

	res := lookPathProbe(ac)
	if !res.Available || ac.Type == TypeExec {
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), detectBudget)
	defer cancel()
	res.Version = probeVersion(ctx, ac)
	return res
}

// lookPathProbe is THE availability rule, shared by every detect path.
//
// It reports availability from PATH resolution and NEVER from a child process'
// exit code: a slow-starting CLI, a first-run wizard or an auth prompt would probe
// as a FALSE NEGATIVE, and a false negative means the agent disappears from the
// caps report and its jobs get rejected. Being on PATH is exactly the condition for
// being launchable (exec.Command and exec.LookPath share one lookup), it has no
// side effects, and it costs microseconds.
func lookPathProbe(ac config.AgentConfig) DetectResult {
	// exec runs the request's argv verbatim and needs no external CLI. This check
	// MUST come before anything that consults a command — including a configured
	// detect block: the shipped configs probe exec with `sh -c true`, and a Windows
	// host has no `sh`, so probing exec at all would report the BUILT-IN exec agent
	// unavailable.
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

// probeVersion runs the agent's version probe and returns its first output line;
// "" on any failure (not found / non-zero exit / timeout / budget exhausted).
//
// It is BEST-EFFORT COSMETICS. The caller has already decided availability via
// lookPathProbe; this only ever adds a display string, so every failure path here
// is a silent "" — it must never be turned into an availability verdict.
//
// The probe defaults to `<command> --version` when no detect block is configured.
// It is bounded by detectTimeout and by whatever remains of the caller's ctx
// (the batch budget), whichever fires first.
func probeVersion(ctx context.Context, ac config.AgentConfig) string {
	cmd, args := ac.Detect.Command, ac.Detect.Args
	if cmd == "" {
		cmd, args = ac.Command, []string{"--version"}
	}
	if cmd == "" {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, cmd, args...).Output()
	if err != nil {
		return ""
	}
	return firstNonEmptyLine(string(out))
}

// firstNonEmptyLine returns the first trimmed non-empty line of s; "" if none.
// Version CLIs usually print a single line (e.g. "codex 0.4.1").
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
