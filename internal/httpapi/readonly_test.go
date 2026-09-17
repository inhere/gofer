package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newReadOnlyServer wires a server whose project allows one read-only-capable
// cli-agent: the agent echoes its argv (the test binary), so a read-only submit can be
// observed from the child's own view. The read-only args are declared explicitly here —
// this test is about the HTTP contract, not about the built-in table.
func newReadOnlyServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"reader"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"reader": {
				Type: agent.TypeCLIAgent, Command: testcmd.Path(t),
				Args:         []string{"argv", "{{prompt}}"},
				ReadOnlyArgs: []string{"--read-only"},
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil)
}

// TestSubmitJobReadOnly: POST /v1/jobs accepts read_only, the job row keeps it (the
// child really received the sandbox flag), and a GET of the finished job still reports
// it — the flag is a persisted property, not an echo of the request.
func TestSubmitJobReadOnly(t *testing.T) {
	s := newReadOnlyServer(t)

	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "reader", Runner: "local",
		Prompt: "audit this", Cwd: ".", TimeoutSec: 30, ReadOnly: true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if !created.ReadOnly {
		t.Fatalf("created job = %+v, want read_only:true", created)
	}

	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("status=%s (err=%s), want done", final.Status, final.Error)
	}
	if !final.ReadOnly {
		t.Fatalf("finished job = %+v, want read_only:true persisted", final)
	}

	rr := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID, testToken, nil)
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d, want 200", rr.StatusCode)
	}
	var fetched job.JobResult
	decode(t, rr, &fetched)
	if !fetched.ReadOnly {
		t.Fatalf("GET /v1/jobs/{id} = %+v, want read_only:true", fetched)
	}

	// A plain submit is still writable (the field is optional, not defaulted on).
	plain := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "reader", Runner: "local",
		Prompt: "audit this", Cwd: ".", TimeoutSec: 30,
	})
	var plainJob job.JobResult
	decode(t, plain, &plainJob)
	if plainJob.ReadOnly {
		t.Fatalf("a submit without read_only = %+v, want read_only:false", plainJob)
	}
	waitDone(t, s, plainJob.ID)
}
