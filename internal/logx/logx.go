// Package logx configures the process-wide structured logger (slog) for gofer.
// A single text handler on stderr is installed as the slog default so any package
// can log key lifecycle events via slog.Info/Warn/Error without threading a logger
// through every struct. stderr (not stdout) keeps logs out of the CLI's data
// output (job JSON, completion scripts, etc.).
package logx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
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
		ReplaceAttr: replaceAttr,
	})
	setupMu.Lock()
	if fileSink != nil {
		_ = fileSink.Close()
		fileSink = nil
	}
	stderrHandler = h
	if operationID == "" {
		operationID = runID()
	}
	setupMu.Unlock()
	slog.SetDefault(slog.New(h))
}

// FileOptions describes a rotating JSONL sink for one process component.
type FileOptions struct {
	Path                              string
	MaxSizeMB, MaxAgeDays, MaxBackups int
	Explicit                          bool
	Component                         string
}

var setupMu sync.Mutex
var stderrHandler slog.Handler
var operationID string
var fileSink *lumberjack.Logger

// ConfigureFile adds a rotating JSONL sink while retaining the text stderr
// sink. Explicit paths fail startup when they cannot be opened; implicit
// defaults warn and continue with stderr only.
func ConfigureFile(o FileOptions) error {
	if strings.TrimSpace(o.Path) == "" {
		return nil
	}
	if o.MaxSizeMB <= 0 {
		o.MaxSizeMB = 50
	}
	if o.MaxAgeDays <= 0 {
		o.MaxAgeDays = 14
	}
	if o.MaxBackups <= 0 {
		o.MaxBackups = 10
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0755); err != nil {
		return sinkFailure(o, err)
	}
	f, err := os.OpenFile(o.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return sinkFailure(o, err)
	}
	_ = f.Close()
	w := &lumberjack.Logger{Filename: o.Path, MaxSize: o.MaxSizeMB, MaxAge: o.MaxAgeDays, MaxBackups: o.MaxBackups, LocalTime: true}
	file := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(os.Getenv("GOFER_LOG_LEVEL")), ReplaceAttr: replaceAttr})
	setupMu.Lock()
	defer setupMu.Unlock()
	if fileSink != nil {
		_ = fileSink.Close()
	}
	if stderrHandler == nil {
		stderrHandler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(os.Getenv("GOFER_LOG_LEVEL")), ReplaceAttr: replaceAttr})
	}
	if operationID == "" {
		operationID = runID()
	}
	fileSink = w
	attrs := []slog.Attr{slog.String("operation_id", operationID), slog.String("component", o.Component)}
	slog.SetDefault(slog.New(multiHandler{handlers: []slog.Handler{stderrHandler.WithAttrs(attrs), file.WithAttrs(attrs)}}))
	return nil
}
func sinkFailure(o FileOptions, e error) error {
	if o.Explicit {
		fmt.Fprintf(os.Stderr, "gofer log file %s: %v\n", o.Path, e)
		return e
	}
	fmt.Fprintf(os.Stderr, "gofer log file warning %s: %v; using stderr only\n", o.Path, e)
	return nil
}
func runID() string {
	b := make([]byte, 6)
	if _, e := rand.Read(b); e == nil {
		return hex.EncodeToString(b)
	}
	return fmt.Sprint(time.Now().UnixNano())
}

// shortenTime reformats the top-level time attribute to timeLayout (dropping the
// zone offset). Group-nested time values and non-time attrs pass through.
func shortenTime(groups []string, a slog.Attr) slog.Attr {
	return replaceAttr(groups, a)
}
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	k := strings.ToLower(a.Key)
	for _, s := range []string{"token", "authorization", "password", "secret"} {
		if strings.Contains(k, s) {
			a.Value = slog.StringValue("***")
			break
		}
	}
	if len(groups) == 0 && a.Key == slog.TimeKey {
		if t, ok := a.Value.Any().(time.Time); ok {
			a.Value = slog.StringValue(t.Format(timeLayout))
		}
	}
	return a
}

// multiHandler fans records to all configured sinks and preserves the
// slog.Handler contract for Enabled, attributes, and groups.
type multiHandler struct{ handlers []slog.Handler }

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}
func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var e error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if x := h.Handle(ctx, r.Clone()); x != nil {
				e = x
			}
		}
	}
	return e
}
func (m multiHandler) WithAttrs(a []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithAttrs(a)
	}
	return multiHandler{out}
}
func (m multiHandler) WithGroup(g string) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithGroup(g)
	}
	return multiHandler{out}
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
