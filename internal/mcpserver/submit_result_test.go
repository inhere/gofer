package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestSubmitResultTool: a job reports its own outcome through the tool (job id from
// GOFER_JOB_ID), the report is stored, a second report is refused, and a different job's
// id is refused.
func TestSubmitResultTool(t *testing.T) {
	session, jobs, target := connectWakeup(t)
	t.Setenv(envJobID, target.ID)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "gofer_submit_result",
		Arguments: map[string]any{"decision": "rework", "reason": "tests are missing", "findings": []string{"no tests"}},
	})
	if msg := toolRefusalText(t, res, err); msg != "" {
		t.Fatalf("submit_result refused: %s", msg)
	}
	var out submitResultOutput
	structured(t, res, &out)
	if out.JobID != target.ID || out.Decision != "rework" || out.Attempt != 1 {
		t.Fatalf("submit_result output = %+v", out)
	}
	got, ok := jobs.Get(target.ID)
	if !ok || got.ResultDecision != "rework" || got.ResultReason != "tests are missing" {
		t.Fatalf("stored result = %+v", got)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "gofer_submit_result",
		Arguments: map[string]any{"decision": "pass", "reason": "again"},
	})
	if msg := toolRefusalText(t, res, err); !strings.Contains(msg, "already reported") {
		t.Fatalf("second report refusal = %q, want 'already reported'", msg)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "gofer_submit_result",
		Arguments: map[string]any{"job_id": "some-other-job", "decision": "pass", "reason": "x"},
	})
	if msg := toolRefusalText(t, res, err); !strings.Contains(msg, "own") {
		t.Fatalf("other job's id refusal = %q, want it refused as not own", msg)
	}
}

// TestSubmitResultOnLeaderAndNotSteward: the leader surface carries the tool (a leader job
// is a job too); the steward surface does not.
func TestSubmitResultOnLeaderAndNotSteward(t *testing.T) {
	names := func(session *mcp.ClientSession) map[string]bool {
		tools, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		m := map[string]bool{}
		for _, tool := range tools.Tools {
			m[tool.Name] = true
		}
		return m
	}
	jobs, projects, agents := testCore(t)
	t.Setenv(envJobID, "job-x")

	t.Setenv(envLeaderPlan, "plan-1")
	if !names(connectTo(t, NewLocal(jobs, projects, agents)))["gofer_submit_result"] {
		t.Fatal("leader surface lacks gofer_submit_result")
	}
	t.Setenv(envLeaderPlan, "")
	if !names(connectTo(t, NewLocal(jobs, projects, agents)))["gofer_submit_result"] {
		t.Fatal("full surface lacks gofer_submit_result")
	}
	t.Setenv(envSteward, "1")
	if names(connectTo(t, NewLocal(jobs, projects, agents)))["gofer_submit_result"] {
		t.Fatal("steward surface must not carry gofer_submit_result")
	}
}
