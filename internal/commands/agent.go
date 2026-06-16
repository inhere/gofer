package commands

import "github.com/gookit/gcli/v3"

// notImplemented returns a placeholder command handler that reports the
// command name and the phase it lands in. Shared by P1 skeleton commands.
func notImplemented(name, phase string) gcli.RunnerFunc {
	return func(c *gcli.Command, args []string) error {
		c.Printf("%s: not implemented (%s)\n", name, phase)
		return nil
	}
}

// NewAgentCmd builds the `agent` command group (list/detect/show). P3 logic.
func NewAgentCmd() *gcli.Command {
	return &gcli.Command{
		Name: "agent",
		Desc: "Inspect configured agents",
		Subs: []*gcli.Command{
			{
				Name: "list",
				Desc: "List configured agents",
				Func: notImplemented("agent list", "P3"),
			},
			{
				Name: "detect",
				Desc: "Run detect commands and report agent availability",
				Func: notImplemented("agent detect", "P3"),
			},
			{
				Name: "show",
				Desc: "Show an agent's configuration",
				Func: notImplemented("agent show", "P3"),
			},
		},
	}
}
