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
	// Split off the raw command tail after `--` BEFORE gcli parses, so `job run
	// ... -- go version` keeps ["go","version"] intact instead of binding the
	// first token to a positional/subcommand. The tail is stashed for `job run`.
	head, raw := commands.SplitRawArgs(os.Args[1:])
	commands.SetRawCmd(raw)
	// Reorder remaining args so a positional <key> placed before flags still
	// parses (gcli/stdlib flag parsing stops at the first positional). See
	// args.go. Propagate the command's exit code (gcli derives a non-zero code
	// only from errorx.ErrorCoder errors, e.g. serve refusing to start without a
	// token).
	os.Exit(app.Run(commands.NormalizeArgs(app, head)))
}
