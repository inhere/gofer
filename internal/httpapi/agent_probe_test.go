package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestAgentProbeSubmitsSyncJob: POST /v1/agents/{key}/probe runs a real, ordinary job
// (a --sync submit with the fixed probe prompt and a tag) and reports its outcome, so
// "is this agent alive right now?" is answered by the same path a job takes — and the
// probe itself counts towards health. The project is resolved when the caller gives
// none, and an agent no project admits is refused instead of submitted.
func TestAgentProbeSubmitsSyncJob(t *testing.T) {
	bin := testcmd.Path(t)
	agents := map[string]config.AgentConfig{
		"codex": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", "OK"}},
		"lone":  {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", "OK"}},
	}
	projects := map[string]config.ProjectConfig{
		"alpha": {HostPath: t.TempDir(), AllowedAgents: []string{"codex"}, AllowedRunners: []string{"local"}},
		"beta":  {HostPath: t.TempDir(), AllowedAgents: []string{"codex"}, AllowedRunners: []string{"local"}},
	}
	s, _ := newAgentTestServer(t, agents, projects)

	resp := do(t, s, http.MethodPost, "/v1/agents/codex/probe", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		JobID      string `json:"job_id"`
		Status     string `json:"status"`
		ExitCode   int    `json:"exit_code"`
		DurationMs int64  `json:"duration_ms"`
		FirstLine  string `json:"first_line"`
	}
	decode(t, resp, &out)
	if out.JobID == "" || out.Status != job.StatusDone || out.ExitCode != 0 {
		t.Fatalf("probe result = %+v, want a done job with exit 0", out)
	}
	if out.FirstLine != "OK" {
		t.Fatalf("first_line = %q, want the agent's own output", out.FirstLine)
	}
	if out.DurationMs < 0 {
		t.Fatalf("duration_ms = %d, want a non-negative duration", out.DurationMs)
	}

	// The probe IS a job: tagged, titled, and run on a project that admits the agent.
	// With no project given it is the first admitted one (sorted).
	probe, ok := s.jobs.Get(out.JobID)
	if !ok {
		t.Fatalf("probe job %s not found", out.JobID)
	}
	if probe.ProjectKey != "alpha" {
		t.Fatalf("probe project = %q, want the first project admitting the agent (alpha)", probe.ProjectKey)
	}
	if probe.Title != "probe codex" {
		t.Fatalf("probe title = %q, want %q", probe.Title, "probe codex")
	}
	if len(probe.Tags) != 1 || probe.Tags[0] != "probe" {
		t.Fatalf("probe tags = %v, want [probe]", probe.Tags)
	}

	// An explicit project is honoured.
	resp = do(t, s, http.MethodPost, "/v1/agents/codex/probe", testToken, map[string]any{"project": "beta"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe with project status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &out)
	if p, _ := s.jobs.Get(out.JobID); p.ProjectKey != "beta" {
		t.Fatalf("probe project = %q, want the requested beta", p.ProjectKey)
	}

	// Unknown agent: nothing to probe.
	resp = do(t, s, http.MethodPost, "/v1/agents/ghost/probe", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("probe of an unknown agent = %d, want 404", resp.StatusCode)
	}
	// Known agent no project admits: refused, never submitted.
	resp = do(t, s, http.MethodPost, "/v1/agents/lone/probe", testToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("probe of an unadmitted agent = %d, want 400", resp.StatusCode)
	}
}
