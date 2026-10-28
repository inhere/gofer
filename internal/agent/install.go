package agent

import "runtime"

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

// InstallInfoFor returns the official installation hint for command on goos.
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
