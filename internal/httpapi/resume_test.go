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
)

// TestResumeStatusMapping covers the pure sentinel→status mapper (resumeStatus):
// unknown job → 404; the three resume-rejection sentinels → 400; an unknown
// project (surfaced by the inner Submit) → 404 via submitStatus; anything else →
// 400.
func TestResumeStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unknown-job", job.ErrUnknownJob, http.StatusNotFound},
		{"no-session", job.ErrNoSession, http.StatusBadRequest},
		{"resume-unsupported", job.ErrResumeUnsupported, http.StatusBadRequest},
		{"cross-runner", job.ErrCrossRunner, http.StatusBadRequest},
		{"unknown-project", job.ErrUnknownProject, http.StatusNotFound},
		{"invalid-request", job.ErrInvalidRequest, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if got := resumeStatus(tc.err); got != tc.want {
			t.Errorf("%s: resumeStatus = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestResumeUnknownJob: POST /v1/jobs/{id}/resume for a non-existent id is a 404.
func TestResumeUnknownJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs/no-such-job/resume", testToken, resumeJobReq{Prompt: "hi"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resume unknown job status=%d, want 404", resp.StatusCode)
	}
}

// TestResumeNoSession: resuming an exec job (which never captures a session_id)
// is a 400 (ErrNoSession).
func TestResumeNoSession(t *testing.T) {
	s := newTestServer(t, testToken, false)
	// Submit a quick exec job and let it finish (no session_id is ever set).
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30, Sync: true,
	})
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("submit exec job: no id (%+v)", created)
	}

	rr := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/resume", testToken, resumeJobReq{Prompt: "again"})
	if rr.StatusCode != http.StatusBadRequest {
		t.Fatalf("resume no-session status=%d, want 400", rr.StatusCode)
	}
}

// TestResumeClaudeJobReturnsLinkedJob: a claude job (inject) resumed over HTTP
// returns 200 with a NEW job whose session_id links back to the source. Uses a
// bespoke server whose claude/codex Command is the harmless `echo` so nothing
// real runs.
func TestResumeClaudeJobReturnsLinkedJob(t *testing.T) {
	s := newResumeTestServer(t)

	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, Sync: true,
	})
	var src job.JobResult
	decode(t, resp, &src)
	if src.SessionID == "" {
		t.Fatalf("claude source job has no session_id (%+v)", src)
	}

	rr := do(t, s, http.MethodPost, "/v1/jobs/"+src.ID+"/resume", testToken, resumeJobReq{Prompt: "what number"})
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("resume status=%d, want 200", rr.StatusCode)
	}
	var newJob job.JobResult
	decode(t, rr, &newJob)
	if newJob.ID == src.ID {
		t.Fatalf("resume returned the SAME job id %q, want a new job", newJob.ID)
	}
	if newJob.SessionID != src.SessionID {
		t.Fatalf("resumed session_id = %q, want %q (linked)", newJob.SessionID, src.SessionID)
	}
	if newJob.Agent != agent.ExecAgentKey {
		t.Fatalf("resumed agent = %q, want exec", newJob.Agent)
	}
}

// TestResumeCrossRunnerRejected: an explicit runner differing from the source is
// a 400 over HTTP.
func TestResumeCrossRunnerRejected(t *testing.T) {
	s := newResumeTestServer(t)

	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30, Sync: true,
	})
	var src job.JobResult
	decode(t, resp, &src)

	rr := do(t, s, http.MethodPost, "/v1/jobs/"+src.ID+"/resume", testToken,
		resumeJobReq{Prompt: "again", Runner: "some-other-runner"})
	if rr.StatusCode != http.StatusBadRequest {
		t.Fatalf("resume cross-runner status=%d, want 400", rr.StatusCode)
	}
}

// newResumeTestServer builds a Server whose claude/codex agents use `echo` as
// their command (so source jobs run fast and never invoke a real CLI) while the
// built-in claude inject / codex capture+resume session defaults still apply.
func newResumeTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"claude", "codex", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true, // resume submits an exec job
			},
		},
		Agents: map[string]config.AgentConfig{
			"claude": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"-p", "{{prompt}}"}},
			"codex":  {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}},
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
