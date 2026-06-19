package main

import (
	"os"

	"github.com/inhere/gofer/internal/commands"
	"github.com/inhere/gofer/internal/config"
)

// Build metadata, injected by Makefile LDFLAGS (-X main.Version etc.).
var (
	Version   string
	GitCommit string
	BuildDate string
)

func main() {
	// Load dotenv files (<config-dir>/.env then ./.env) before anything reads the
	// environment, so GOFER_CONFIG/GOFER_TOKEN etc. can come from a
	// file. Exported OS env still wins; a malformed .env is non-fatal.
	_, _ = config.LoadDotenv()

	app := commands.NewApp(Version)
	// Reorder args so a positional <key>/<id> placed before flags still parses
	// (gcli/stdlib flag parsing stops at the first positional). NormalizeArgs
	// leaves `--` and the raw tail after it untouched, so gcli handles `--`
	// natively (the tokens reach `job run` as remainArgs). Propagate the
	// command's exit code (gcli derives a non-zero code only from
	// errorx.ErrorCoder errors, e.g. serve refusing to start without a token).
	os.Exit(app.Run(commands.NormalizeArgs(app, os.Args[1:])))
}
