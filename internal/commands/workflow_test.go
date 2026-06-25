package commands

import (
	"testing"
)

// TestWorkflowSubcommandsRegistered asserts the P3-a `workflow` group is wired
// into the app with its run/show/list/cancel subcommands (and reachable via the
// `wf` alias).
func TestWorkflowSubcommandsRegistered(t *testing.T) {
	app := NewApp("test")

	wfCmd := app.GetCommand("workflow")
	if wfCmd == nil {
		t.Fatal("workflow command not registered")
	}
	for _, sub := range []string{"run", "show", "list", "cancel", "export", "events"} {
		if wfCmd.GetCommand(sub) == nil {
			t.Fatalf("workflow subcommand %q not registered", sub)
		}
	}
	// The `wf` alias resolves to the workflow command group.
	if !app.IsAlias("wf") || app.ResolveAlias("wf") != "workflow" {
		t.Fatalf("`wf` alias should resolve to workflow, got %q", app.ResolveAlias("wf"))
	}
	// `add` is an alias for `run` (so `wf add file` == `wf run file`): gcli stores
	// subcommand aliases on the group and resolves them at run time, so assert via
	// IsAlias/ResolveAlias (GetCommand is name-only, not alias-aware).
	if !wfCmd.IsAlias("add") || wfCmd.ResolveAlias("add") != "run" {
		t.Fatalf("`add` should be an alias for the run subcommand, got %q", wfCmd.ResolveAlias("add"))
	}
}

// TestWorkflowExitCodeMapping pins the --watch exit-code mapping: done=0,
// cancelled=130, failed/other=1 (mirrors job watch).
func TestWorkflowExitCodeMapping(t *testing.T) {
	cases := map[string]int{
		"done":      0,
		"cancelled": 130,
		"failed":    1,
		"running":   1,
		"":          1,
	}
	for status, want := range cases {
		if got := workflowExitCode(status); got != want {
			t.Fatalf("workflowExitCode(%q)=%d want %d", status, got, want)
		}
	}
}
