package mcpserver

import (
	"context"
	"testing"
)

func TestSessionJobMCPSayAndEnd(t *testing.T) {
	session, _ := connect(t)
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{"gofer_job_say": false, "gofer_job_end": false}
	for _, tool := range result.Tools {
		if _, ok := found[tool.Name]; ok {
			found[tool.Name] = true
		}
	}
	for name, ok := range found {
		if !ok {
			t.Errorf("MCP tool %s missing", name)
		}
	}
}
