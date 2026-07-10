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

	app.Add(NewInitCmd())
	app.Add(NewConfigCmd())
	app.Add(NewServeCmd(info))
	app.Add(NewProjectCmd())
	app.Add(NewAgentCmd())
	app.Add(NewPresenceCmd())
	app.Add(NewJobCmd())
	app.Add(NewWorkflowCmd())
	app.Add(NewPlanCmd())
	app.Add(NewScheduleCmd())
	app.Add(NewMcpCmd())
	app.Add(NewWorkerCmd())

	return app
}
