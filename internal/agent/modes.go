package agent

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// Modes reports whether an agent supports batch and interactive execution.
//
// Batch is every agent that is not declared interactive-only: exec agents and
// any cli-agent without the legacy `interactive: true`. A cli-agent whose args
// carry no {{prompt}} is still a batch agent — wrappers that take the prompt
// from stdin/env, or probes like `go env`, have always run that way and the
// only thing the old reverse gate rejected was `interactive: true`.
// Interactive is `interactive_args` (an empty list means a bare TUI launch) or
// the legacy flag, whose args ARE the interactive argv.
func Modes(ac config.AgentConfig) (batch, interactive bool) {
	if ac.Type == TypeExec {
		return true, false
	}
	return !ac.Interactive, ac.InteractiveArgs != nil || ac.Interactive
}

// ValidateConfig checks mode combinations shared by server and worker loading.
func ValidateConfig(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	for key, ac := range cfg.Agents {
		if ac.Interactive && hasPrompt(ac.Args) {
			return fmt.Errorf("agent %q: interactive with args containing {{prompt}}; use interactive_args for a dual-mode agent", key)
		}
		if hasPrompt(ac.InteractiveArgs) {
			return fmt.Errorf("agent %q: interactive_args must not contain {{prompt}}", key)
		}
		if ac.Type == TypeExec && ac.InteractiveArgs != nil {
			return fmt.Errorf("agent %q: type exec cannot set interactive_args", key)
		}
	}
	return nil
}

func hasPrompt(args []string) bool {
	for _, arg := range args {
		if strings.Contains(arg, "{{prompt}}") {
			return true
		}
	}
	return false
}
