package agent

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// detectTimeout bounds how long a single detect probe may run.
const detectTimeout = 5 * time.Second

// DetectResult reports an agent's availability. A failed probe (missing CLI,
// non-zero exit, timeout) sets Available=false and Error; it MUST NOT panic or
// abort service startup (plan §9-P3, §11).
type DetectResult struct {
	Available bool
	Version   string
	Error     string
}

// Detect runs the agent's configured detect command and reports availability.
//
// Semantics:
//   - Unknown agent -> Available=false with an error.
//   - exec agent with no detect command -> Available=true (it just runs the
//     request argv directly; no external CLI dependency).
//   - Any other agent with no detect command -> Available=false (cannot probe).
//   - Probe failure (binary not found / non-zero exit / timeout) -> Available
//     =false with the captured error; never panics.
func (r *Registry) Detect(agentKey string) DetectResult {
	ac, ok := r.Get(agentKey)
	if !ok {
		return DetectResult{Available: false, Error: "unknown agent " + agentKey}
	}

	if ac.Detect.Command == "" {
		// The built-in exec agent has no external CLI to probe; it is always
		// usable on its own. Any other agent without a detect command cannot be
		// probed, so report unavailable rather than claim availability.
		if ac.Type == TypeExec {
			return DetectResult{Available: true, Version: "builtin"}
		}
		return DetectResult{Available: false, Error: "no detect command configured"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), detectTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ac.Detect.Command, ac.Detect.Args...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return DetectResult{Available: false, Error: "detect timed out after " + detectTimeout.String()}
		}
		return DetectResult{Available: false, Error: err.Error()}
	}

	version := firstNonEmptyLine(string(out))
	return DetectResult{Available: true, Version: version}
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
