package commands

import "github.com/gookit/gcli/v3"

// NewApp assembles the agent-bridge gcli application and registers all
// top-level commands (serve/project/agent/job/mcp).
func NewApp(version string) *gcli.App {
	app := gcli.NewApp()
	app.Name = "agent-bridge"
	app.Desc = "Bridge local and container CLI agents across allowed projects."
	if version != "" {
		app.Version = version
	}

	app.Add(NewServeCmd())
	app.Add(NewProjectCmd())
	app.Add(NewAgentCmd())
	app.Add(NewJobCmd())
	app.Add(NewMcpCmd())

	return app
}
