package httpapi

import (
	"net/http"
	"strings"
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

// TestSubmitJobFromSession (gofer-f4z8): POST /v1/jobs takes the snake_case
// from_session, the agent's from_session_args reach the argv, and GET of the job reports
// it back from the persisted request; an agent without the fragment is a 400.
func TestSubmitJobFromSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"heir", "plain"}, AllowedRunners: []string{"local"},
		}},
		Agents: map[string]config.AgentConfig{
			"heir": {
				Type: agent.TypeCLIAgent, Command: testcmd.Path(t),
				Args:            []string{"argv", "{{prompt}}"},
				FromSessionArgs: []string{"--from", "{{from_session}}"},
			},
			"plain": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"argv", "{{prompt}}"}},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil)

	body := map[string]any{
		"project_key": "self", "agent": "heir", "runner": "local",
		"prompt": "continue", "cwd": ".", "timeout_sec": 30, "from_session": "s-old",
	}
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.FromSession != "s-old" {
		t.Fatalf("created = %+v, want from_session s-old", created)
	}
	waitDone(t, s, created.ID)

	rr := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID, testToken, nil)
	var fetched job.JobResult
	decode(t, rr, &fetched)
	if fetched.FromSession != "s-old" || !strings.Contains(fetched.RenderedCommand, `"--from","s-old"`) {
		t.Fatalf("GET job: from_session=%q cmd=%q", fetched.FromSession, fetched.RenderedCommand)
	}

	// gofer-ldmp: GET /v1/agents exposes the capability so the console can offer
	// (or grey out) 「从此会话新开」.
	ar := do(t, s, http.MethodGet, "/v1/agents", testToken, nil)
	var listed struct {
		Agents []agentView `json:"agents"`
	}
	decode(t, ar, &listed)
	caps := map[string]bool{}
	for _, a := range listed.Agents {
		caps[a.Key] = a.FromSession
	}
	if !caps["heir"] || caps["plain"] {
		t.Fatalf("from_session caps = %v, want heir=true plain=false", caps)
	}

	body["agent"] = "plain"
	if bad := do(t, s, http.MethodPost, "/v1/jobs", testToken, body); bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("agent without from_session_args: status=%d, want 400", bad.StatusCode)
	}
}
