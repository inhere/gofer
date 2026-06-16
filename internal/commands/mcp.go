package commands

import "github.com/gookit/gcli/v3"

// NewMcpCmd builds the `mcp` command (stdio MCP server). P8 logic.
func NewMcpCmd() *gcli.Command {
	return &gcli.Command{
		Name: "mcp",
		Desc: "Run the stdio MCP server",
		Func: notImplemented("mcp", "P8"),
	}
}
