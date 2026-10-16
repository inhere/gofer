package mcpserver

import (
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

func exerciseCaptureTool(t *testing.T, s *mcp.ClientSession, meta *jobstore.Store) {
	t.Helper()
	var d work.DetailView
	structured(t, callTool(t, s, "gofer_work_capture", map[string]any{"text": "想加个导出", "kind": "idea", "project": "self"}), &d)
	if d.ID == "" || d.Status != jobstore.WorkCaptured || d.Kind != jobstore.WorkKindIdea || d.ProjectKey != "self" {
		t.Fatalf("capture = %+v", d.WorkItem)
	}
	// Default kind note.
	structured(t, callTool(t, s, "gofer_work_capture", map[string]any{"text": "随手记"}), &d)
	if d.Kind != jobstore.WorkKindNote || d.Status != jobstore.WorkCaptured {
		t.Fatalf("default kind: %+v", d.WorkItem)
	}
	// Refused: bad kind, blank text.
	if res := callTool(t, s, "gofer_work_capture", map[string]any{"text": "x", "kind": "bogus"}); !res.IsError {
		t.Fatal("an invalid kind must be an error result")
	}
	if res := callTool(t, s, "gofer_work_capture", map[string]any{"text": " "}); !res.IsError {
		t.Fatal("blank text must be an error result")
	}
	items, err := meta.ListWorkItems(jobstore.WorkListOpts{Statuses: []string{jobstore.WorkCaptured}})
	if err != nil || len(items) != 2 {
		t.Fatalf("captured items = %d (%v), want 2", len(items), err)
	}
}

func TestWorkCaptureToolLocalBackend(t *testing.T) {
	jobs, projects, agents := testCore(t)
	s := connectTo(t, newServer(newLocalBackend(jobs, projects, agents), ""))
	exerciseCaptureTool(t, s, jobs.Meta())
}

func TestWorkCaptureToolClientBackend(t *testing.T) {
	jobs, projects, agents := testCore(t)
	srv := httptest.NewServer(httpapi.New(&config.ServerConfig{AllowEmptyToken: true}, "", true, jobs, nil, projects, agents, nil, nil, nil, nil).Handler())
	t.Cleanup(srv.Close)
	s := connectTo(t, newServer(NewClientBackend(client.New(srv.URL, "")), ""))
	exerciseCaptureTool(t, s, jobs.Meta())
}

func TestWorkCaptureToolProjectScope(t *testing.T) {
	jobs, projects, agents := testCore(t)
	s := connectTo(t, newServer(newLocalBackend(jobs, projects, agents), "self"))
	var d work.DetailView
	structured(t, callTool(t, s, "gofer_work_capture", map[string]any{"text": "范围内"}), &d)
	if d.ProjectKey != "self" {
		t.Fatalf("a scoped capture must land in the scoped project: %+v", d.WorkItem)
	}
	if res := callTool(t, s, "gofer_work_capture", map[string]any{"text": "越界", "project": "elsewhere"}); !res.IsError {
		t.Fatal("capturing into another project must fail under a scope")
	}
}
