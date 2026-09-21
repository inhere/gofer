package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestRunJobCollectParam: gofer_run_job forwards collect globs (XFER-01 X2) into the
// submitted request — the server matches them on the executing machine when the job
// ends, so an MCP caller gets the same file-carrying job as the CLI.
func TestRunJobCollectParam(t *testing.T) {
	session, jobs := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         testcmd.Cmd(t, "exit", "0"),
			"cwd":         ".",
			"timeout_sec": 30,
			"collect":     []string{"tmp/out/*.csv"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	final, ok := jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &req); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if len(req.Collect) != 1 || req.Collect[0] != "tmp/out/*.csv" {
		t.Fatalf("collect did not round-trip through MCP: %v", req.Collect)
	}
}
