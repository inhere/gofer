package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// reviewCore builds a service whose project runs one resumable cli-agent ("codex", the
// test binary), so a reviewed job can be rejected — and continued with resume.
func reviewCore(t *testing.T) (*job.Service, *project.Registry, *agent.Registry, *presence.Service) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type: agent.TypeCLIAgent, Command: testcmd.Path(t),
				Args:          []string{"stderr-exit", "0", "", "{{prompt}}"},
				SessionResume: []string{"stderr-exit", "0", "resumed {{prompt}}"},
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	jobs := job.NewService(cfg, projects, agents, runners, meta, nil)
	// Nothing may still be writing into the temp dir when the framework removes it: a
	// rejected job with resume=true spawns a continuation that outlives the call.
	t.Cleanup(func() { drainJobs(t, jobs) })
	return jobs, projects, agents, presence.NewService(meta)
}

// submitReviewedJob submits a reviewed job through the service and waits for it to park
// in needs_review (a non-terminal state, so the usual terminal waits do not apply).
func submitReviewedJob(t *testing.T, jobs *job.Service) job.JobResult {
	t.Helper()
	res, err := jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30,
		Review: true, SessionID: "sess-mcp",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, ok := jobs.Get(res.ID)
		if !ok {
			t.Fatalf("job %s vanished", res.ID)
		}
		if cur.Status == job.StatusNeedsReview {
			return cur
		}
		if job.IsTerminal(cur.Status) {
			t.Fatalf("job %s reached %s, want needs_review", res.ID, cur.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never reached needs_review", res.ID)
	return job.JobResult{}
}

// TestRejectJobTool: gofer_reject_job records the agent's refusal with its note (the
// agent identity is attributed) and — with resume — starts the continuation and reports
// its id, so a supervisor can hand the work back for another attempt.
func TestRejectJobTool(t *testing.T) {
	jobs, projects, agents, pres := reviewCore(t)
	session := connectTo(t, NewLocal(jobs, projects, agents, pres))
	created := submitReviewedJob(t, jobs)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_reject_job",
		Arguments: map[string]any{
			"job_id": created.ID,
			"note":   "the tests are still red",
			"resume": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out struct {
		jobView
		ResumeJobID string `json:"resume_job_id"`
	}
	structured(t, res, &out)
	if out.Status != job.StatusRejected {
		t.Fatalf("reject view status = %q, want rejected", out.Status)
	}
	if out.ReviewedBy == "" || out.ReviewNote != "the tests are still red" {
		t.Fatalf("reject view = %+v, want the reviewer and note recorded", out)
	}
	if out.ResumeJobID == "" {
		t.Fatalf("reject resume must report the continuation: %+v", out)
	}

	cont, ok := jobs.Get(out.ResumeJobID)
	if !ok {
		t.Fatalf("continuation %s not found", out.ResumeJobID)
	}
	if cont.ResumedFrom != created.ID {
		t.Fatalf("continuation resumed_from=%q, want %s", cont.ResumedFrom, created.ID)
	}
	// The decision is durable on the source job (the store row, not just the view).
	rec, ok, err := jobs.Meta().GetJob(created.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob: ok=%v err=%v", ok, err)
	}
	if rec.Status != job.StatusRejected || rec.ReviewNote != "the tests are still red" {
		t.Fatalf("persisted row = %+v, want a rejected job with the note", rec)
	}
}

// TestNoAcceptJobTool: an agent must never accept its own work, so the MCP surface has
// NO accept tool at all — a registered agent can only refuse and say why.
func TestNoAcceptJobTool(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	hasReject := false
	for _, tl := range res.Tools {
		if strings.Contains(tl.Name, "accept") {
			t.Fatalf("MCP must not expose an accept tool, found %q", tl.Name)
		}
		if tl.Name == "gofer_reject_job" {
			hasReject = true
		}
	}
	if !hasReject {
		t.Fatalf("gofer_reject_job missing from ListTools")
	}
}

// TestRejectJobToolUnknownJob: a bad job id surfaces as a tool error (the handler does
// not invent a success), which is what makes the refusal auditable.
func TestRejectJobToolUnknownJob(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_reject_job",
		Arguments: map[string]any{"job_id": "nope", "note": "no"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("rejecting an unknown job must be a tool error: %+v", res.StructuredContent)
	}
}

// TestRejectJobOverHTTPBackend: the MCP tool must work against a REMOTE central serve
// too (clientBackend), not only in-process — the runner sends the note/resume and reads
// the continuation id back from the HTTP response.
func TestRejectJobOverHTTPBackend(t *testing.T) {
	var gotPath, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.ReviewOutcome{
			JobResult:   job.JobResult{ID: "job-1", Status: job.StatusRejected, ReviewedBy: "mcp"},
			ResumeJobID: "job-2",
		})
	}))
	defer ts.Close()

	session := connectTo(t, New(NewClientBackend(client.New(ts.URL, ""))))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_reject_job",
		Arguments: map[string]any{"job_id": "job-1", "note": "not yet", "resume": true},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out struct {
		jobView
		ResumeJobID string `json:"resume_job_id"`
	}
	structured(t, res, &out)
	if out.ResumeJobID != "job-2" || out.Status != job.StatusRejected {
		t.Fatalf("view = %+v, want the rejected job + its continuation", out)
	}
	if gotPath != "/v1/jobs/job-1/reject" {
		t.Fatalf("path = %q, want the reject endpoint", gotPath)
	}
	if !strings.Contains(gotBody, `"note":"not yet"`) || !strings.Contains(gotBody, `"resume":true`) {
		t.Fatalf("body = %q, want the note + resume flag", gotBody)
	}
}
