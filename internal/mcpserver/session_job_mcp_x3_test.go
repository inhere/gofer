package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
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
	mux := http.NewServeMux()
	var gotMessage string
	mux.HandleFunc("/v1/jobs/j1/say", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotMessage = body.Message
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "j1", Status: job.StatusAwaitingInput, Session: true})
	})
	mux.HandleFunc("/v1/jobs/j1/end", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "j1", Status: job.StatusDone, Session: true})
	})
	remote := connectTo(t, New(mockBackend(t, mux)))
	say, err := remote.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_job_say", Arguments: map[string]any{"job_id": "j1", "message": "second"},
	})
	if err != nil || say.IsError || gotMessage != "second" {
		t.Fatalf("MCP say: err=%v isError=%v message=%q", err, say.IsError, gotMessage)
	}
	ended, err := remote.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_job_end", Arguments: map[string]any{"job_id": "j1"},
	})
	if err != nil || ended.IsError {
		t.Fatalf("MCP end: err=%v result=%+v", err, ended)
	}
	var final jobView
	structured(t, ended, &final)
	if final.Status != job.StatusDone || !final.Session {
		t.Fatalf("MCP end projection=%+v", final)
	}
}
