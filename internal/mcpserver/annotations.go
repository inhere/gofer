package mcpserver

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/mcptools"
)

// addGoferTool registers one of gofer's own MCP tools. It is the ONLY registration
// path in this package (it replaces a bare mcp.AddTool call): it stamps the tool with
// the MCP `annotations` its entry in internal/mcptools declares, so every client sees
// readOnlyHint / destructiveHint / idempotentHint — and so the approval gate, which
// reads the same registry, agrees with what clients are told.
func addGoferTool[In, Out any](s *mcp.Server, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if err := annotateGoferTool(tool); err != nil {
		// A registration-time programming error, not a runtime condition: an
		// unclassified tool would either be auto-approved with side effects or keep
		// parking a human on a harmless list call. Fail the build's tests instead.
		panic(err)
	}
	mcp.AddTool(s, tool, h)
}

// annotateGoferTool fills tool.Annotations from the registry. The hints are always
// explicit (never left to the protocol defaults): readOnlyHint is false by default, so
// a write tool that forgot it would read as one that mutates — but a read-only tool
// must SAY so for a client (and suga) not to gate it.
func annotateGoferTool(tool *mcp.Tool) error {
	t, ok := mcptools.Lookup(tool.Name)
	if !ok {
		return fmt.Errorf("mcpserver: gofer MCP tool %q is not classified in internal/mcptools", tool.Name)
	}
	readOnly := t.Class == mcptools.ReadOnly
	destructive := t.Class == mcptools.Destructive
	tool.Annotations = &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  readOnly || t.Idempotent,
	}
	return nil
}
