// Package logx configures the process-wide structured logger (slog) for gofer.
// A single text handler on stderr is installed as the slog default so any package
// can log key lifecycle events via slog.Info/Warn/Error without threading a logger
// through every struct. stderr (not stdout) keeps logs out of the CLI's data
// output (job JSON, completion scripts, etc.).
package logx

import (
	"log/slog"
	"os"
	"strings"
)

// Setup installs the default slog logger. The level comes from GOFER_LOG_LEVEL
// (debug | info | warn | error; default info); an unrecognised value falls back
// to info. Called once at process start (cmd/gofer/main).
func Setup() {
	lvl := parseLevel(os.Getenv("GOFER_LOG_LEVEL"))
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(h))
}

// parseLevel maps GOFER_LOG_LEVEL to a slog.Level (default info).
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
