package commands

import (
	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/config"
)

// NewApp assembles the gofer gcli application and registers all
// top-level commands (serve/project/agent/job/mcp/worker).
func NewApp(version string) *gcli.App {
	app := gcli.NewApp()
	app.Name = "gofer"
	app.Desc = "Run CLI agents and commands as async jobs across projects and remote workers - HTTP/CLI/MCP control plane with a built-in web console."
	if version != "" {
		app.Version = version
	}

	// add global config option for all commands
	app.Flags().StrOpt(&config.InputCfgFile, "config", "c", "${GOFER_CONFIG}", "path to the gofer config file")

	app.Add(NewInitCmd())
	app.Add(NewConfigCmd())
	app.Add(NewServeCmd())
	app.Add(NewProjectCmd())
	app.Add(NewAgentCmd())
	app.Add(NewJobCmd())
	app.Add(NewWorkflowCmd())
	app.Add(NewMcpCmd())
	app.Add(NewWorkerCmd())
	app.Add(NewStopCmd())

	return app
}
