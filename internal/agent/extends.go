package agent

import (
	"sort"

	"github.com/inhere/gofer/internal/config"
)

// Register the built-in templates with the config package, which resolves
// `extends: builtin` while loading (it cannot import this package, G022). The
// returned value is copied by config before use.
func init() {
	config.BuiltinAgentTemplate = func(name string) (config.AgentConfig, bool) {
		tpl, ok := builtinTemplates[name]
		return tpl, ok
	}
	config.BuiltinAgentTemplateNames = func() []string {
		names := make([]string, 0, len(builtinTemplates))
		for k := range builtinTemplates {
			names = append(names, k)
		}
		sort.Strings(names)
		return names
	}
}
