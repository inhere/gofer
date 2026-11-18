package commands

import "os"

// stdoutIsTerminal reports whether this process's stdout is a terminal (a character
// device), the one predicate the CLI uses to tell an interactive run from a piped one —
// a job, an install script or any `cmd | tee` runs the command with a pipe and must not
// get per-line progress output meant for a person watching the screen.
//
// A var, not a func, so a test can pin both shapes without a real terminal (G053).
var stdoutIsTerminal = func() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
