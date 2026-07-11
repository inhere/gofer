package commands

import (
	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
)

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
	addGroup("Setup & config", NewInitCmd(), NewConfigCmd(), NewProjectCmd(), NewAgentCmd(), NewMcpCmd())
	addGroup("Control plane", NewServeCmd(info), NewPresenceCmd(), NewWorkerCmd())
	addGroup("Jobs & workflows", NewJobCmd(), NewWorkflowCmd(), NewPlanCmd(), NewScheduleCmd())

	// Quickstart hint after the command list, so a new user has a path in.
	app.HelpConfig.AfterCmdText = "\n<comment>Quickstart:</>\n" +
		"  gofer init                 # scaffold config (server or worker node)\n" +
		"  gofer serve                # start the control-plane server\n" +
		"  gofer job run -- <cmd>     # submit a command as an async job\n" +
		"  # worker node instead: gofer worker init  then  gofer worker\n\n"

	return app
}
