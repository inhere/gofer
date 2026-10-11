package commands

import (
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// CFG-16 helpers: a runner's declared external tools on the worker's capability
// report and in `gofer worker show`.

// toolBriefs converts declared tools to their wire form (nil for none).
func toolBriefs(tools []config.Tool) []wsproto.ToolBrief {
	if len(tools) == 0 {
		return nil
	}
	out := make([]wsproto.ToolBrief, 0, len(tools))
	for _, t := range tools {
		out = append(out, wsproto.ToolBrief{Name: t.Name, Desc: t.Desc, Bin: t.Bin})
	}
	return out
}

// printWorkerTools prints the worker's declared tools: one `tools:` line of names,
// or with verbose one indented line per tool with its executable and description.
// Nothing is printed when the worker declares none.
func printWorkerTools(c *gcli.Command, tools []client.RunnerTool, verbose bool) {
	if len(tools) == 0 {
		return
	}
	if !verbose {
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t.Name)
		}
		c.Printf("tools: %s\n", strings.Join(names, ", "))
		return
	}
	c.Printf("tools:\n")
	for _, t := range tools {
		line := "  - " + t.Name
		if t.Bin != "" && t.Bin != t.Name {
			line += " (" + t.Bin + ")"
		}
		if t.Desc != "" {
			line += ": " + t.Desc
		}
		c.Printf("%s\n", line)
	}
}
