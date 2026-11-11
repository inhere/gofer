package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/streaming"
)

// transcriptServer serves a two-page transcript of job-t and a detail response with
// turn_count; seen records the transcript query strings it was asked.
func transcriptServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/job-t/transcript":
			*seen = append(*seen, r.URL.RawQuery)
			page := streaming.JobTranscriptPage{Source: "turns", Mode: "turns", Total: 2}
			if r.URL.Query().Get("cursor") == "" {
				page.Turns = []streaming.JobTranscriptTurn{{Turn: 1, Role: "assistant", Text: "hello ", Chars: 11}}
				page.Truncated, page.Next = true, &streaming.TranscriptCursor{Turn: 1, Offset: 6}
			} else {
				page.Turns = []streaming.JobTranscriptTurn{{Turn: 1, Role: "assistant", Text: "world", Offset: 6, Chars: 11}}
			}
			_ = json.NewEncoder(w).Encode(page)
		case "/v1/jobs/job-t":
			_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-t", ProjectKey: "p", Status: job.StatusDone, TurnCount: 3})
		case "/v1/jobs/job-t/logs/stdout":
			_, _ = w.Write([]byte("log body\n"))
		case "/v1/jobs/job-t/wakeups", "/v1/jobs/job-t/retries":
			_, _ = w.Write([]byte("[]"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// `job transcript` through the real command dispatch: one page printed with the cursor
// hint; --out follows the cursor and writes the whole text to a local file.
func TestJobTranscriptCommandPrintsPageAndExportsWithOut(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	var seen []string
	ts := transcriptServer(t, &seen)
	defer ts.Close()

	out := captureOutput(t, func() {
		code := NewApp("test").Run([]string{"job", "transcript", "job-t", "--turns", "1", "--server", ts.URL})
		assert.Eq(t, 0, code)
	})
	assert.True(t, strings.Contains(out, "=== turn 1"), out)
	assert.True(t, strings.Contains(out, "hello "), out)
	assert.True(t, strings.Contains(out, "--cursor 1:6"), out)
	assert.Eq(t, "turns=1", seen[0])

	seen = nil
	file := filepath.Join(t.TempDir(), "export.md")
	resetFlagGlobals()
	out = captureOutput(t, func() {
		code := NewApp("test").Run([]string{"job", "transcript", "job-t", "--turns", "1", "--out", file, "--server", ts.URL})
		assert.Eq(t, 0, code)
	})
	assert.True(t, strings.Contains(out, "wrote "+file), out)
	b, err := os.ReadFile(file)
	assert.NoErr(t, err)
	assert.True(t, strings.Contains(string(b), "hello world"), string(b))
	assert.Eq(t, 2, len(seen)) // second request carried the cursor
	assert.True(t, strings.Contains(seen[1], "cursor=1%3A6"), seen[1])
}

// The flags map onto the query the server validates (head/tail zero means "not given").
func TestJobTranscriptCommandFlagsReachTheQuery(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	var seen []string
	ts := transcriptServer(t, &seen)
	defer ts.Close()
	captureOutput(t, func() {
		NewApp("test").Run([]string{"job", "transcript", "job-t", "--tail", "2", "--server", ts.URL})
	})
	assert.Eq(t, "tail=2", seen[0])
	resetFlagGlobals()
	captureOutput(t, func() {
		NewApp("test").Run([]string{"job", "transcript", "job-t", "--search", "needle", "--server", ts.URL})
	})
	assert.Eq(t, "search=needle", seen[1])
}

// A multi-turn job's `job show` ends with the turn-count pointer.
func TestJobShowPrintsTurnHintForMultiTurnJob(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	var seen []string
	ts := transcriptServer(t, &seen)
	defer ts.Close()
	out := captureOutput(t, func() {
		assert.Eq(t, 0, NewApp("test").Run([]string{"job", "show", "job-t", "--server", ts.URL}))
	})
	assert.True(t, strings.Contains(out, "共 3 回合") || strings.Contains(out, "3 turns"), out)
	assert.True(t, strings.Contains(out, "job transcript job-t"), out)
	assert.Eq(t, "", turnHint(job.JobResult{ID: "x", TurnCount: 1}))
}
