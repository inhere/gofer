package main

import (
	"os"

	"dev-agent-bridge/internal/commands"
)

// Build metadata, injected by Makefile LDFLAGS (-X main.Version etc.).
var (
	Version   string
	GitCommit string
	BuildDate string
)

func main() {
	app := commands.NewApp(Version)
	// Reorder args so a positional <key> placed before flags still parses
	// (gcli/stdlib flag parsing stops at the first positional). See args.go.
	// Propagate the command's exit code (gcli derives a non-zero code only from
	// errorx.ErrorCoder errors, e.g. serve refusing to start without a token).
	os.Exit(app.Run(commands.NormalizeArgs(app, os.Args[1:])))
}
