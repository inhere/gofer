package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestRunJobReadOnlyParam: gofer_run_job accepts read_only and it reaches the job
// request (the flag is what sandboxes the agent), and the tool's input schema declares
// it so an MCP client can discover the parameter.
func TestRunJobReadOnlyParam(t *testing.T) {
	session, jobs := connect(t)
	// Add a read-only-capable cli-agent to this test core and allow it in the project.
	cfg := jobs.Config()
	if cfg.Agents == nil {
		cfg.Agents = map[string]config.AgentConfig{}
	}
	cfg.Agents["reader"] = config.AgentConfig{
		Type: agent.TypeCLIAgent, Command: "go", Args: []string{"env"},
		ReadOnlyArgs: []string{"--read-only"},
	}
	p := cfg.Projects["self"]
	p.AllowedAgents = []string{"reader"}
	p.AllowExec = false
	cfg.Projects["self"] = p

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "reader",
			"runner":      "local",
			"prompt":      "audit this",
			"cwd":         ".",
			"timeout_sec": 30,
			"read_only":   true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if !created.ReadOnly {
		t.Fatalf("run_job response = %+v, want read_only:true", created)
	}
	final, ok := jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &req); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if !req.ReadOnly {
		t.Fatalf("read_only did not round-trip through MCP: %+v", req)
	}
	if !final.ReadOnly {
		t.Fatalf("finished job = %+v, want read_only:true", final)
	}

	schema := runJobSchema(t, session)
	if _, ok := schema.Properties["read_only"]; !ok {
		t.Fatalf("input schema missing read_only; properties=%v", schema.Properties)
	}
}

// runJobSchema returns gofer_run_job's input-schema properties as seen by a client.
func runJobSchema(t *testing.T, session *mcp.ClientSession) struct {
	Properties map[string]any `json:"properties"`
} {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	for _, tl := range res.Tools {
		if tl.Name != "gofer_run_job" {
			continue
		}
		b, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatalf("marshal input schema: %v", err)
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatalf("unmarshal input schema: %v", err)
		}
		return schema
	}
	t.Fatalf("gofer_run_job not found")
	return schema
}
