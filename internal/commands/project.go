package commands

import "github.com/gookit/gcli/v3"

// projectAddOpts holds `project add` flags. Logic is implemented in P2.
var projectAddOpts = struct {
	config         string
	hostPath       string
	containerPath  string
	exchangeSubdir string
	resultSubdir   string
	defaultAgent   string
	allowAgents    gcli.Strings // repeatable: --allow-agent
	allowRunner    string
	allowExec      bool
	force          bool
}{}

// NewProjectCmd builds the `project` command group with sub commands
// list/show/add/remove/validate. P1: all sub bodies are placeholders (P2).
func NewProjectCmd() *gcli.Command {
	return &gcli.Command{
		Name: "project",
		Desc: "Manage registered projects",
		Subs: []*gcli.Command{
			{
				Name: "list",
				Desc: "List configured projects",
				Func: notImplemented("project list", "P2"),
			},
			{
				Name: "show",
				Desc: "Show a project's details",
				Func: notImplemented("project show", "P2"),
			},
			{
				Name: "add",
				Desc: "Register a new project",
				Config: func(c *gcli.Command) {
					c.StrOpt(&projectAddOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&projectAddOpts.hostPath, "host-path", "", "", "absolute host path of the project (required)")
					c.StrOpt(&projectAddOpts.containerPath, "container-path", "", "", "container mount path of the project")
					c.StrOpt(&projectAddOpts.exchangeSubdir, "exchange-subdir", "", "tmp", "data exchange subdir under the project")
					c.StrOpt(&projectAddOpts.resultSubdir, "result-subdir", "", "dev-agent-bridge", "result subdir under the exchange subdir")
					c.StrOpt(&projectAddOpts.defaultAgent, "default-agent", "", "", "default agent for this project")
					c.VarOpt(&projectAddOpts.allowAgents, "allow-agent", "", "allowed agent (repeatable)")
					c.StrOpt(&projectAddOpts.allowRunner, "allow-runner", "", "local", "allowed runner")
					c.BoolOpt(&projectAddOpts.allowExec, "allow-exec", "", false, "allow exec agent in this project")
					c.BoolOpt(&projectAddOpts.force, "force", "", false, "overwrite an existing project entry")
				},
				Func: notImplemented("project add", "P2"),
			},
			{
				Name: "remove",
				Desc: "Remove a registered project",
				Func: notImplemented("project remove", "P2"),
			},
			{
				Name: "validate",
				Desc: "Validate a project's paths, agents and runners",
				Func: notImplemented("project validate", "P2"),
			},
		},
	}
}
