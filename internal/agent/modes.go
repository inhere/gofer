package agent

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// Modes reports whether an agent supports batch and interactive execution.
func Modes(ac config.AgentConfig) (batch, interactive bool) {
	if ac.Type == TypeExec {
		return true, false
	}
	for _, arg := range ac.Args {
		if strings.Contains(arg, "{{prompt}}") {
			batch = true
			break
		}
	}
	interactive = ac.InteractiveArgs != nil || ac.Interactive
	return
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
