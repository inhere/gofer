package commands

import (
	"fmt"

	"github.com/gookit/gcli/v3"
)

// completionOpts holds `completion` flags: the target shell (bash|zsh).
var completionOpts = struct {
	shell string
}{}

// NewCompletionCmd builds the `completion` command (E2 P2-c). It prints a static
// shell-completion script for the current command/option tree to stdout, so it
// can be sourced directly:
//
//	gofer completion bash > /etc/bash_completion.d/gofer   # or: source <(gofer completion bash)
//	gofer completion zsh  > "${fpath[1]}/_gofer"
//
// v1 emits the static (embedded) script — the command/option tree is hardcoded
// into the script; regenerate after adding commands/flags. Dynamic id/project
// candidate completion is left for a later iteration. The shell is taken from the
// positional <shell> arg, falling back to --shell, defaulting to bash.
func NewCompletionCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "completion",
		Desc:    "Print a shell-completion script (bash|zsh) to stdout",
		Aliases: []string{"genac"},
		Config: func(c *gcli.Command) {
			c.StrOpt(&completionOpts.shell, "shell", "s", "bash", "target shell: bash|zsh")
			c.AddArg("shell", "target shell: bash|zsh (overrides --shell)", false)
		},
		Func: runCompletion,
	}
}

// runCompletion resolves the target shell (positional arg > --shell > bash) and
// prints the static completion script. An unsupported shell surfaces the gcli
// generator's error.
func runCompletion(c *gcli.Command, _ []string) error {
	shell := completionOpts.shell
	if a := c.Arg("shell"); a != nil && a.String() != "" {
		shell = a.String()
	}
	if shell == "" {
		shell = "bash"
	}
	script, err := c.App().GenStaticCompletionScript(shell, "gofer")
	if err != nil {
		return err
	}
	fmt.Print(script)
	return nil
}
