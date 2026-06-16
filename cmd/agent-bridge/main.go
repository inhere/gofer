package main

import "dev-agent-bridge/internal/commands"

// Build metadata, injected by Makefile LDFLAGS (-X main.Version etc.).
var (
	Version   string
	GitCommit string
	BuildDate string
)

func main() {
	app := commands.NewApp(Version)
	app.Run(nil)
}
