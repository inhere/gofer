package commands

import (
	"testing"

	"github.com/gookit/gcli/v3"
)

func TestSessionJobCLISayAndEnd(t *testing.T) {
	for _, args := range [][]string{
		{"job", "run", "-p", "self", "-a", "acpbot", "--session", "--prompt", "hello"},
		{"job", "say", "session-1", "hello"},
		{"job", "end", "session-1"},
	} {
		jobRunOpts.session = false
		app := NewApp("test")
		name := args[1]
		command := app.GetCommand("job").GetCommand(name)
		if command == nil {
			t.Errorf("job %s command missing", name)
			continue
		}
		command.Func = func(_ *gcli.Command, _ []string) error { return nil }
		if code := app.Run(args); code != 0 {
			t.Errorf("job %s parse exit=%d", name, code)
		}
		if name == "run" && !jobRunOpts.session {
			t.Error("job run --session was not bound")
		}
	}
}
