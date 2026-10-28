package commands

import (
	"runtime"
	"sort"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

func agentInstallHint(key string, ac config.AgentConfig) string {
	info, ok := agent.AgentInstallInfo(key, ac, runtime.GOOS)
	if !ok {
		return ""
	}
	if info.Command == "" {
		return cliText("cli.worker.init.agent_missing_docs", "url", info.URL)
	}
	return cliText("cli.worker.init.agent_missing_hint", "command", info.Command, "url", info.URL)
}

// Configuration validation may run on a different host from the eventual worker,
// so this never changes the validation result.
func printAgentInstallWarnings(c interface{ Printf(string, ...interface{}) }, cfg *config.Config) {
	if cfg == nil {
		return
	}
	detected := agent.DefaultDetector().Detect(cfg.Agents)
	keys := make([]string, 0, len(cfg.Agents))
	for key := range cfg.Agents {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ac := cfg.Agents[key]
		res := detected[key]
		if res.Available {
			continue
		}
		hint := agentInstallHint(key, ac)
		if hint == "" {
			continue
		}
		c.Printf("[WARN] agent/%-14s %s\n", key, hint)
	}
}
