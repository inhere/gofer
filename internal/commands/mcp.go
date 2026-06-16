package commands

import "github.com/gookit/gcli/v3"

// notImplemented returns a placeholder command handler that reports the command
// name and the phase it lands in. Shared by not-yet-implemented commands.
func notImplemented(name, phase string) gcli.RunnerFunc {
	return func(c *gcli.Command, args []string) error {
		c.Printf("%s: not implemented (%s)\n", name, phase)
		return nil
	}
}

// NewMcpCmd builds the `mcp` command (stdio MCP server). P8 logic.
func NewMcpCmd() *gcli.Command {
	return &gcli.Command{
		Name: "mcp",
		Desc: "Run the stdio MCP server",
		Func: notImplemented("mcp", "P8"),
	}
}
