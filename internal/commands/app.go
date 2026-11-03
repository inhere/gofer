package commands

import (
	"path/filepath"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// clientLogFileName is the client request log inside the config dir's run/
// directory (the same run/ that holds serve.log and worker-<id>.log).
const clientLogFileName = "client.log"

// configureClientRequestLog points the client's request-failure log at
// <config-dir>/run/client.log. It runs while the app is assembled, i.e. before
// any command builds a client or sends a request (GR107: the entry layer only
// binds and wires).
//
// It resolves the config dir itself rather than calling config.RuntimeFilePath:
// that helper degrades to a RELATIVE path when the user's home cannot be
// resolved (it must, the daemon relies on that), which would write
// ./run/client.log into whatever directory the CLI happened to run in — inside a
// job, the business repository. An unresolvable config dir therefore means no
// client log at all, not a stray file in the cwd.
func configureClientRequestLog() {
	dir, err := config.ConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		client.SetRequestLogPath("")
		return
	}
	client.SetRequestLogPath(filepath.Join(dir, "run", clientLogFileName))
}

// NewApp assembles the gofer gcli application and registers all
// top-level commands (serve/project/agent/job/mcp/worker). Stopping a
// backgrounded daemon is a subcommand of its starter: `serve stop` / `worker stop`.
func NewApp(version string) *gcli.App {
	return NewAppWithBuildInfo(buildinfo.Info{Version: version})
}

// NewAppWithBuildInfo assembles the CLI with linker build metadata available to
// commands that need to surface it through runtime APIs.
func NewAppWithBuildInfo(info buildinfo.Info) *gcli.App {
	app := gcli.NewApp()
	app.Name = "gofer"
	app.Desc = "Run CLI agents and commands as async jobs across projects and remote workers - HTTP/CLI/MCP control plane with a built-in web console."
	if version := info.DisplayVersion(); version != "" {
		app.Version = version
	}

	// Client request failures (timeouts, 5xx, 401/403) need a trace that outlives
	// the command's stderr; wire it here so every command has it.
	configureClientRequestLog()

	// add global config option for all commands
	app.Flags().StrOpt(&config.InputCfgFile, "config", "c", "${GOFER_CONFIG}", "path to the gofer config file")

	// Group commands by role in --help. gcli renders one section per Category
	// (in category insertion order), so the group order below is intentional:
	// onboarding (setup) -> run the plane -> submit work.
	addGroup := func(category string, cmds ...*gcli.Command) {
		for _, c := range cmds {
			c.Category = category
			app.Add(c)
		}
	}
	addGroup("Setup & config", NewInitCmd(info), NewConfigCmd(), NewSettingsCmd(), NewProjectCmd(), NewAgentCmd(), NewMcpCmd(), NewHookCmd(), NewDoctorCmd(info))
	addGroup("Repository tracking", NewRepoCmd(), NewIssueCmd(), NewMemoryCmd())
	addGroup("Control plane", NewServeCmd(info), NewWorkerCmd(info), NewTunnelCmd(), NewAuthCmd())
	addGroup("Jobs & workflows", NewJobCmd(), NewWorkflowCmd(), NewPlanCmd(), NewScheduleCmd(), NewSessionCmd(), NewWorkCmd(), NewStewardCmd(), NewTemplateCmd())
	// G033: small utility commands live under `gofer tool` (never a new top-level
	// command) — currently the XFER-01 file transfer (`tool cp` / `tool xfer`).
	addGroup("Utilities", NewToolCmd())

	// Quickstart hint after the command list, so a new user has a path in.
	app.HelpConfig.AfterCmdText = "\n<comment>Quickstart:</>\n" +
		"  gofer init                 # scaffold config (server or worker node)\n" +
		"  gofer serve                # start the control-plane server\n" +
		"  gofer job run -- <cmd>     # submit a command as an async job\n" +
		"  # worker node instead: gofer worker init  then  gofer worker\n\n"

	return app
}
