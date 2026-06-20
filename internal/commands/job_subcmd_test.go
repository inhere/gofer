package commands

import "testing"

// TestJobSubcommandsRegistered asserts the E2 P2-c subcommands (list/watch/
// rerun) are wired into the `job` group alongside the existing run/show/logs/
// cancel, and that the top-level `completion` command exists.
func TestJobSubcommandsRegistered(t *testing.T) {
	app := NewApp("test")

	jobCmd := app.GetCommand("job")
	if jobCmd == nil {
		t.Fatal("job command not registered")
	}
	for _, sub := range []string{"run", "show", "logs", "cancel", "list", "watch", "rerun"} {
		if jobCmd.GetCommand(sub) == nil {
			t.Fatalf("job subcommand %q not registered", sub)
		}
	}

	if app.GetCommand("completion") == nil {
		t.Fatal("completion command not registered")
	}
}

// TestTerminalExitCodeMapping pins the watch exit-code mapping: done=0,
// cancelled=130, every other terminal (failed/timeout/unknown)=1.
func TestTerminalExitCodeMapping(t *testing.T) {
	cases := map[string]int{
		"done":      0,
		"cancelled": 130,
		"failed":    1,
		"timeout":   1,
		"":          1,
	}
	for status, want := range cases {
		if got := terminalExitCode(status); got != want {
			t.Fatalf("terminalExitCode(%q)=%d want %d", status, got, want)
		}
	}
}
