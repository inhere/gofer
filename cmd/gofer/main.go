package main

import (
	"os"

	"github.com/gookit/goutil/x/ccolor"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/commands"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/logx"
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
	_, err := config.LoadDotenv()
	if err != nil {
		ccolor.Warnln("Load dotenv error:", err)
	}

	// Install the structured logger (slog) before anything logs, so worker/hub
	// connect-lifecycle events surface. Level via GOFER_LOG_LEVEL (default info).
	logx.Setup()

	app := commands.NewAppWithBuildInfo(buildinfo.Info{
		Version:   Version,
		GitCommit: GitCommit,
		BuildDate: BuildDate,
	})
	os.Exit(app.Run(os.Args[1:]))
}
