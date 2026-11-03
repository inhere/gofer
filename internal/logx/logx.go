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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// TimeLayout is the log timestamp format. It drops slog's default RFC3339 zone
// offset (the trailing "+08:00") — local time with millis is enough and far
// shorter to scan: 2026-06-27T15:09:21.473. It is exported so a value written INTO a
// log line (a deadline, an expected wake time) is formatted like the line's own
// timestamp and can be read as a time, not as a unix number.
const TimeLayout = "2006-01-02T15:04:05.000"

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

// SilenceStderr drops the stderr text sink for the rest of the process: a
// later ConfigureFile keeps only its file sink, and until then the default
// logger discards everything. It is what `gofer tunnel forward --quiet` means:
// nothing on the terminal, the file log untouched.
func SilenceStderr() {
	h := slog.NewTextHandler(io.Discard, nil)
	setupMu.Lock()
	stderrHandler = h
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

// fillDefaults applies the rotation defaults shared by ConfigureFile and
// OpenFileSink (50MB / 14 days / 10 backups).
func fillDefaults(o *FileOptions) {
	if o.MaxSizeMB <= 0 {
		o.MaxSizeMB = 50
	}
	if o.MaxAgeDays <= 0 {
		o.MaxAgeDays = 14
	}
	if o.MaxBackups <= 0 {
		o.MaxBackups = 10
	}
}

// ConfigureFile adds a rotating JSONL sink while retaining the text stderr
// sink. Explicit paths fail startup when they cannot be opened; implicit
// defaults warn and continue with stderr only.
func ConfigureFile(o FileOptions) error {
	if strings.TrimSpace(o.Path) == "" {
		return nil
	}
	fillDefaults(&o)
	// Mint the operation id BEFORE taking setupMu: ensureOperationID takes the same
	// lock, so asking for it while holding it would deadlock (the id itself is
	// process-wide and set at most once).
	opID := ensureOperationID()
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
	fileSink = w
	attrs := []slog.Attr{slog.String("operation_id", opID), slog.String("component", o.Component)}
	slog.SetDefault(slog.New(dedupeHandler{Handler: multiHandler{handlers: []slog.Handler{stderrHandler.WithAttrs(attrs), file.WithAttrs(attrs)}}, keys: attrKeys(attrs)}))
	return nil
}

// FileSink is a standalone rotating JSONL sink: a logger that writes only to its
// own file. Unlike ConfigureFile it installs NOTHING globally — the process
// default logger (usually the stderr text handler) is left exactly as it was, so
// adding a component log can never change a command's stdout/stderr contract.
// Rotation, redaction, time layout and the level (GOFER_LOG_LEVEL) are the same
// as ConfigureFile's file sink.
//
// A sink is meant to be closed after a batch of records (the client request log
// closes after every line): writes are synchronous, so nothing is lost, and a
// component log never keeps a handle on the user's file — or on a temp config dir
// in a test — between lines.
type FileSink struct {
	lg *slog.Logger
	w  *lumberjack.Logger
}

// OpenFileSink opens the rotating JSONL file o.Path and returns a sink writing to
// it only. It fails when the path cannot be created — the caller decides whether
// that is fatal (the client's request log treats it as "log disabled").
func OpenFileSink(o FileOptions) (*FileSink, error) {
	if strings.TrimSpace(o.Path) == "" {
		return nil, fmt.Errorf("logx: empty log path")
	}
	fillDefaults(&o)
	if err := os.MkdirAll(filepath.Dir(o.Path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(o.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	w := &lumberjack.Logger{Filename: o.Path, MaxSize: o.MaxSizeMB, MaxAge: o.MaxAgeDays, MaxBackups: o.MaxBackups, LocalTime: true}
	var attrs []any
	if id := ensureOperationID(); id != "" {
		attrs = append(attrs, "operation_id", id)
	}
	if o.Component != "" {
		attrs = append(attrs, "component", o.Component)
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(os.Getenv("GOFER_LOG_LEVEL")), ReplaceAttr: replaceAttr})
	return &FileSink{lg: slog.New(h).With(attrs...), w: w}, nil
}

// Logger is the sink's logger; every record it accepts lands in the file (the
// underlying write is synchronous, so a short-lived process loses nothing on
// exit).
func (s *FileSink) Logger() *slog.Logger { return s.lg }

// Close releases the file handle. The sink stays USABLE: lumberjack reopens the
// file on the next write (and re-checks the rotation size), so closing after each
// line is the way a component sink keeps from holding a handle it does not need.
func (s *FileSink) Close() error { return s.w.Close() }

// ensureOperationID returns this process's operation id, minting one when no
// Setup/ConfigureFile ran yet (a component sink opened before the default one).
func ensureOperationID() string {
	setupMu.Lock()
	defer setupMu.Unlock()
	if operationID == "" {
		operationID = runID()
	}
	return operationID
}

// attrKeys is the key set of the handler-level attrs ConfigureFile stamps on
// every record.
func attrKeys(as []slog.Attr) map[string]struct{} {
	keys := make(map[string]struct{}, len(as))
	for _, a := range as {
		keys[a.Key] = struct{}{}
	}
	return keys
}

// dedupeHandler keeps one record free of duplicate keys.
//
// ConfigureFile stamps operation_id and component on every record, but call
// sites still pass their own "component" ("server", "worker", ...) — the same
// key twice in one JSONL line is invalid for strict parsers and ambiguous for a
// reader (
// "component":"serve",...,"component":"server"). The HANDLER value wins: it
// names the process that actually wrote the line, which is what an operator
// filtering serve.log / worker-<id>.log is after. A call site therefore cannot
// override the component; it drops the duplicate instead of the authority.
//
// Only top-level record attrs are deduplicated: a key inside a group is a
// different key path and is left alone.
//
// OpenFileSink does not wrap its handler in this: it prepends the same
// operation_id / component once (via With) and the only writer it serves — the
// client request log — never passes a component of its own (internal/client
// reqlog.go).
type dedupeHandler struct {
	slog.Handler
	keys map[string]struct{}
}

func (d dedupeHandler) Handle(ctx context.Context, r slog.Record) error {
	n := 0
	r.Attrs(func(a slog.Attr) bool {
		if _, ok := d.keys[a.Key]; ok {
			n++
		}
		return true
	})
	if n == 0 {
		return d.Handler.Handle(ctx, r)
	}
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if _, ok := d.keys[a.Key]; !ok {
			out.AddAttrs(a)
		}
		return true
	})
	return d.Handler.Handle(ctx, out)
}

func (d dedupeHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return d
	}
	keys := make(map[string]struct{}, len(d.keys)+len(as))
	for k := range d.keys {
		keys[k] = struct{}{}
	}
	for _, a := range as {
		keys[a.Key] = struct{}{}
	}
	return dedupeHandler{Handler: d.Handler.WithAttrs(as), keys: keys}
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

// shortenTime reformats the top-level time attribute to TimeLayout (dropping the
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
			a.Value = slog.StringValue(t.Format(TimeLayout))
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
