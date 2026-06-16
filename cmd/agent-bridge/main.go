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
	app.Run(commands.NormalizeArgs(app, os.Args[1:]))
}
