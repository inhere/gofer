package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/logx"
)

// This file is the client's request log: one JSONL line per request that went
// WRONG (transport failure, 5xx, 401/403) in <config-dir>/run/client.log, and —
// with GOFER_LOG_LEVEL=debug — one line per request that went right. It exists
// because a CLI failure used to leave no trace anywhere: `session topic` failing
// with "context deadline exceeded (Client.Timeout exceeded while awaiting
// headers)" was invisible on both sides, so nobody could tell whether the server
// or the client was slow.
//
// Rules it keeps:
//   - never the body, headers, bearer token or query string (SR1301) — only the
//     method, the query-less path, the status, the elapsed time and a short
//     error summary;
//   - the process default logger (stderr) is untouched, so a command's
//     stdout/stderr — and every golden test of it — stays byte-identical;
//   - a sink that cannot be opened disables the log instead of failing the
//     request (a diagnostic must never break the work it describes).

// requestRecorder owns the client request log file. One per process.
type requestRecorder struct {
	mu   sync.Mutex
	path string // "" = disabled (the default outside the CLI entry point)
	sink *logx.FileSink
	lg   *slog.Logger
	// broken is set once opening the sink failed: every later request would fail
	// the same way, and retrying it per request would cost an open() each time.
	broken bool
	// fails counts CONSECUTIVE failures per "METHOD path", which is what the
	// `retry` field reports: 0 on the first failure, 1 on the second in a row.
	// Bounded (see bump) because a diagnostic map must not grow forever.
	fails map[string]int
}

var reqLog = &requestRecorder{}

// maxTrackedRequests bounds the consecutive-failure map; a client that touches
// more distinct paths than this only loses the "how many times in a row" hint.
const maxTrackedRequests = 512

// SetRequestLogPath names the file client request failures are appended to. The
// CLI entry point points it at <config-dir>/run/client.log; the default (empty)
// disables the log, which is what keeps a library embedder or a test process
// from writing into the user's config dir behind the caller's back.
//
// The sink is opened lazily on the first loggable request and then kept: writes
// are synchronous (lumberjack), so a short-lived CLI process loses nothing when
// it exits right after a failure.
func SetRequestLogPath(path string) {
	reqLog.mu.Lock()
	defer reqLog.mu.Unlock()
	path = strings.TrimSpace(path)
	if reqLog.path == path {
		return
	}
	if reqLog.sink != nil {
		// Closing a log file is a best-effort teardown with nobody left to report
		// to (the process is repointing or exiting); the line, if any, is already
		// on disk.
		_ = reqLog.sink.Close()
		reqLog.sink = nil
	}
	reqLog.lg = nil
	reqLog.broken = false
	reqLog.path = path
}

// logger returns the request logger, opening the sink on first use. nil means
// "logging disabled" (no path configured, or the file cannot be written).
func (r *requestRecorder) logger() *slog.Logger {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lg != nil {
		return r.lg
	}
	if r.path == "" || r.broken {
		return nil
	}
	sink, err := logx.OpenFileSink(logx.FileOptions{Path: r.path, Component: "client"})
	if err != nil {
		// A broken diagnostic sink must not fail the request it was describing, so
		// this error is reported once on stderr and then dropped (GR109's exception:
		// there is nowhere else to put it, and the request outcome is the caller's).
		r.broken = true
		fmt.Fprintf(os.Stderr, "gofer client log %s: %v; client request log disabled\n", r.path, err)
		return nil
	}
	r.sink, r.lg = sink, sink.Logger()
	return r.lg
}

// bump records one failure for key and reports how many consecutive ones came
// before it (0 for the first).
func (r *requestRecorder) bump(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fails == nil {
		r.fails = make(map[string]int)
	}
	n := r.fails[key]
	if len(r.fails) >= maxTrackedRequests {
		r.fails = make(map[string]int)
	}
	r.fails[key] = n + 1
	return n
}

// clear forgets key's failure streak (a success ends it).
func (r *requestRecorder) clear(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fails != nil {
		delete(r.fails, key)
	}
}

// logRequest records one finished HTTP request (or tunnel dial). It is a no-op
// unless the log is configured and the outcome is worth a line: a transport
// failure or a 5xx (ERROR), a 401/403 (WARN) — a business 4xx is an answer, not
// an incident, and is not logged. In GOFER_LOG_LEVEL=debug every request is
// recorded, successes included.
//
// status is 0 when the request never got a response (transport error).
func (c *Client) logRequest(method, path string, status int, start time.Time, err error) {
	lg := reqLog.logger()
	if lg == nil {
		return
	}
	key := method + " " + requestPath(path)
	ctx := context.Background()
	var level slog.Level
	event := "client.request_failed"
	switch {
	case err != nil, status >= 500:
		level = slog.LevelError
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		level = slog.LevelWarn
	case status > 0 && status < 400:
		level, event = slog.LevelDebug, "client.request_succeeded"
	default:
		return // a 4xx business answer: the caller reports it, nothing to record
	}
	retry := 0
	if event == "client.request_failed" {
		retry = reqLog.bump(key)
	} else {
		reqLog.clear(key)
	}
	if !lg.Enabled(ctx, level) {
		return
	}
	summary := ""
	if err != nil {
		summary = errorSummary(err)
	} else if status >= 400 {
		summary = http.StatusText(status)
	}
	attrs := []any{
		"event", event,
		"method", method,
		"path", key[len(method)+1:],
		"status", status,
		"elapsed_ms", time.Since(start).Milliseconds(),
		"retry", retry,
	}
	if summary != "" {
		attrs = append(attrs, "error", summary)
	}
	switch event {
	case "client.request_succeeded":
		lg.Log(ctx, level, "client request", attrs...)
	default:
		lg.Log(ctx, level, "client request failed", attrs...)
	}
}

// requestPath is the path as the log carries it: no query string (SR1301 — a
// query can hold filters, tokens or ids the log has no business keeping) and a
// bounded length, so one absurd id cannot bloat the line.
func requestPath(path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	const maxPath = 200
	if len(path) > maxPath {
		path = path[:maxPath] + "…"
	}
	return path
}

// errorSummary is the log's one-line error text: the transport cause without the
// request URL (net/http wraps failures in *url.Error, whose message embeds the
// full URL including the query), with query strings scrubbed and the length
// capped.
func errorSummary(err error) string {
	cause := err
	for {
		var ue *url.Error
		if !errors.As(cause, &ue) || ue.Err == nil {
			break
		}
		cause = ue.Err
	}
	msg := strings.Join(strings.Fields(cause.Error()), " ")
	msg = stripQuery(msg)
	const maxMsg = 300
	if len(msg) > maxMsg {
		msg = msg[:maxMsg] + "…"
	}
	return msg
}

// stripQuery drops every "?…" run up to the next space or quote from msg, so an
// error that still carries a URL cannot leak its query string.
func stripQuery(msg string) string {
	for {
		i := strings.IndexByte(msg, '?')
		if i < 0 {
			return msg
		}
		j := i + 1
		for j < len(msg) && msg[j] != ' ' && msg[j] != '"' && msg[j] != '\'' {
			j++
		}
		msg = msg[:i] + msg[j:]
	}
}
