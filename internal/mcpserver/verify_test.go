package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestRunJobVerifyParams: gofer_run_job exposes the verify step (SUP-01 B) as
// verify / verify_timeout_sec / no_verify, the schema advertises them (so an agent
// can discover the capability), and the values reach the submitted job verbatim —
// the verify argv runs and its result comes back on the job view.
func TestRunJobVerifyParams(t *testing.T) {
	session, jobs := connect(t)

	schema := runJobSchema(t, session)
	for _, key := range []string{"verify", "verify_timeout_sec", "no_verify"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("gofer_run_job schema is missing %q; properties=%v", key, schema.Properties)
		}
	}

	argv := testcmd.Cmd(t, "exit", "0")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         testcmd.Cmd(t, "exit", "0"),
			"cwd":         ".",
			"timeout_sec": 30,
			"verify":      argv,
			// 1s is enough for `exit 0`; a tight value also proves the field is
			// honoured rather than ignored (an ignored timeout would fall back to 600).
			"verify_timeout_sec": 20,
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
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Verify == nil || final.Verify.Status != job.VerifyPassed {
		t.Fatalf("verify = %+v, want the passed step", final.Verify)
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &req); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if len(req.Verify) != len(argv) || req.Verify[0] != argv[0] {
		t.Fatalf("verify did not round-trip through MCP: %#v want %#v", req.Verify, argv)
	}
	if req.VerifyTimeoutSec != 20 {
		t.Fatalf("verify_timeout_sec did not round-trip through MCP: %d want 20", req.VerifyTimeoutSec)
	}

	// no_verify is the opt-out of a project default: it must survive the tool call.
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         testcmd.Cmd(t, "exit", "0"),
			"cwd":         ".",
			"timeout_sec": 30,
			"no_verify":   true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job (no_verify): %v", err)
	}
	var offCreated jobView
	structured(t, res, &offCreated)
	off, ok := jobs.Wait(offCreated.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", offCreated.ID)
	}
	var offReq job.JobRequest
	if err := json.Unmarshal([]byte(off.RequestJSON), &offReq); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if !offReq.NoVerify {
		t.Fatalf("no_verify did not round-trip through MCP: %+v", offReq)
	}
}
