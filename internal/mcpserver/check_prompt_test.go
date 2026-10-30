package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// TestRunJobCheckPromptParam: gofer_run_job carries the delivery-check requirement into
// the job (source defaults to explicit) and the input schema declares both fields.
func TestRunJobCheckPromptParam(t *testing.T) {
	session, jobs := connect(t)
	cfg := jobs.Config()
	if cfg.Agents == nil {
		cfg.Agents = map[string]config.AgentConfig{}
	}
	cfg.Agents["reader"] = config.AgentConfig{Type: agent.TypeCLIAgent, Command: "go", Args: []string{"env"}}
	p := cfg.Projects["self"]
	p.AllowedAgents = []string{"reader"}
	p.AllowExec = false
	cfg.Projects["self"] = p

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "reader", "runner": "local", "prompt": "audit this",
			"cwd": ".", "timeout_sec": 30, "check_prompt": "lists every changed file",
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
	if final.CheckPrompt != "lists every changed file" || final.CheckPromptSource != "explicit" {
		t.Fatalf("job check = %q / %q, want the requirement with source explicit", final.CheckPrompt, final.CheckPromptSource)
	}
	schema := runJobSchema(t, session)
	for _, k := range []string{"check_prompt", "check_prompt_source"} {
		if _, ok := schema.Properties[k]; !ok {
			t.Fatalf("input schema missing %s; properties=%v", k, schema.Properties)
		}
	}
}
