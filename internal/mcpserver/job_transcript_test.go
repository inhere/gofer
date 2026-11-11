package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/streaming"
)

// gofer_get_job_transcript pages a job's per-turn record (read-only, same mode rules as
// the CLI / HTTP), and refuses a bad parameter combination.
func TestGetJobTranscriptTool(t *testing.T) {
	session, jobs := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 30},
	})
	assert.NoErr(t, err)
	var created jobView
	structured(t, res, &created)
	final, ok := jobs.Wait(created.ID)
	assert.True(t, ok)
	assert.NoErr(t, os.WriteFile(filepath.Join(final.ResultDir, store.TurnsFile), []byte(
		`{"turn":1,"role":"assistant","text":"alpha"}`+"\n"+`{"turn":2,"role":"assistant","text":"beta"}`+"\n"), 0o644))

	call := func(args map[string]any) (*mcp.CallToolResult, streaming.JobTranscriptPage) {
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_get_job_transcript", Arguments: args})
		assert.NoErr(t, err)
		var page streaming.JobTranscriptPage
		if !r.IsError {
			structured(t, r, &page)
		}
		return r, page
	}
	_, page := call(map[string]any{"id": created.ID})
	assert.Eq(t, 2, page.Total)
	assert.Eq(t, "alpha", page.Turns[0].Preview)
	_, page = call(map[string]any{"id": created.ID, "turns": "2"})
	assert.Eq(t, "beta", page.Turns[0].Text)
	_, page = call(map[string]any{"id": created.ID, "tail": 1})
	assert.Eq(t, 2, page.Turns[0].Turn)
	_, page = call(map[string]any{"id": created.ID, "search": "alp"})
	assert.Eq(t, 1, len(page.Turns))

	bad, _ := call(map[string]any{"id": created.ID, "head": 1, "tail": 1})
	assert.True(t, bad.IsError)
	missing, _ := call(map[string]any{"id": "job-nope"})
	assert.True(t, missing.IsError)
}
