package commands

import "testing"

func TestPlanSubcommandsRegistered(t *testing.T) {
	app := NewApp("test")

	planCmd := app.GetCommand("plan")
	if planCmd == nil {
		t.Fatal("plan command not registered")
	}
	for _, sub := range []string{"create", "list", "show", "attach"} {
		if planCmd.GetCommand(sub) == nil {
			t.Fatalf("plan subcommand %q not registered", sub)
		}
	}
	if !planCmd.IsAlias("ls") || planCmd.ResolveAlias("ls") != "list" {
		t.Fatalf("`ls` should be an alias for plan list, got %q", planCmd.ResolveAlias("ls"))
	}
}
