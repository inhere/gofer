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
	"time"
)

// timeLayout is the log timestamp format. It drops slog's default RFC3339 zone
// offset (the trailing "+08:00") — local time with millis is enough and far
// shorter to scan: 2026-06-27T15:09:21.473.
const timeLayout = "2006-01-02T15:04:05.000"

// Setup installs the default slog logger. The level comes from GOFER_LOG_LEVEL
// (debug | info | warn | error; default info); an unrecognised value falls back
// to info. Called once at process start (cmd/gofer/main).
func Setup() {
	lvl := parseLevel(os.Getenv("GOFER_LOG_LEVEL"))
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:       lvl,
		ReplaceAttr: shortenTime,
	})
	slog.SetDefault(slog.New(h))
}

// shortenTime reformats the top-level time attribute to timeLayout (dropping the
// zone offset). Group-nested time values and non-time attrs pass through.
func shortenTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		if t, ok := a.Value.Any().(time.Time); ok {
			a.Value = slog.StringValue(t.Format(timeLayout))
		}
	}
	return a
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
