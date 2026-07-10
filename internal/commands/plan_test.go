package commands

import (
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
)

func TestPlanSubcommandsRegistered(t *testing.T) {
	app := NewApp("test")

	planCmd := app.GetCommand("plan")
	if planCmd == nil {
		t.Fatal("plan command not registered")
	}
	for _, sub := range []string{"create", "list", "show", "attach", "add-todo", "set-todo"} {
		if planCmd.GetCommand(sub) == nil {
			t.Fatalf("plan subcommand %q not registered", sub)
		}
	}
	if !planCmd.IsAlias("ls") || planCmd.ResolveAlias("ls") != "list" {
		t.Fatalf("`ls` should be an alias for plan list, got %q", planCmd.ResolveAlias("ls"))
	}
	if !planCmd.IsAlias("todo-add") || planCmd.ResolveAlias("todo-add") != "add-todo" {
		t.Fatalf("`todo-add` should be an alias for plan add-todo, got %q", planCmd.ResolveAlias("todo-add"))
	}
	if !planCmd.IsAlias("todo-done") || planCmd.ResolveAlias("todo-done") != "set-todo" {
		t.Fatalf("`todo-done` should be an alias for plan set-todo, got %q", planCmd.ResolveAlias("todo-done"))
	}
}

func TestPrintPlanTodos(t *testing.T) {
	out := captureOutput(t, func() {
		printPlanTodos(gcli.NewCommand("show", "", nil), []client.Todo{
			{TodoID: "todo-1", Title: "done todo", Done: true, JobID: "job-1"},
			{TodoID: "todo-2", Title: "plain todo"},
		})
	})
	for _, want := range []string{"todos:", "[x]", "todo-1", "done todo", "(job=job-1)", "[ ]", "todo-2", "plain todo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("printPlanTodos output missing %q:\n%s", want, out)
		}
	}
}
