package agent

import (
	"runtime"

	"github.com/inhere/gofer/internal/config"
)

// InstallInfo describes an official, non-interactive installation hint for an
// agent executable. Commands are intentionally suggestions only; gofer never
// executes them.
type InstallInfo struct {
	Command string
	URL     string
}

var installInfo = map[string]InstallInfo{
	"claude":   {Command: "curl -fsSL https://claude.ai/install.sh | bash", URL: "https://code.claude.com/docs/en/setup"},
	"codex":    {Command: "npm install -g @openai/codex", URL: "https://github.com/openai/codex#installing-and-running-codex-cli"},
	"opencode": {Command: "npm install -g opencode-ai", URL: "https://opencode.ai/docs/"},
	"gemini":   {Command: "npm install -g @google/gemini-cli", URL: "https://github.com/google-gemini/gemini-cli"},
	"omp":      {Command: "curl -fsSL https://omp.sh/install | sh", URL: "https://github.com/can1357/oh-my-pi#install"},
}

// AgentInstallInfo resolves the official hint for a configured agent.
func AgentInstallInfo(key string, ac config.AgentConfig, goos string) (InstallInfo, bool) {
	return InstallInfoForAgent(key, ac.Command, goos)
}

// InstallInfoForAgent resolves the command used by an agent config. ACP templates
// are named after their underlying CLI, while their launcher is often npx.
func InstallInfoForAgent(key, command, goos string) (InstallInfo, bool) {
	if info, ok := InstallInfoFor(command, goos); ok && command != "npx" {
		return info, true
	}
	base := key
	for len(base) > 4 && base[len(base)-4:] == "-acp" {
		base = base[:len(base)-4]
	}
	return InstallInfoFor(base, goos)
}

// An empty command means the upstream documentation is known but no command is
// asserted by this project. Unknown executables return false.
func InstallInfoFor(command, goos string) (InstallInfo, bool) {
	info, ok := installInfo[command]
	if !ok {
		return InstallInfo{}, false
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "windows" && goos != "linux" && goos != "darwin" {
		info.Command = ""
	} else if goos == "windows" {
		switch command {
		case "claude":
			info.Command = "irm https://claude.ai/install.ps1 | iex"
		case "omp":
			info.Command = "irm https://omp.sh/install.ps1 | iex"
		}
	}
	return info, true
}
