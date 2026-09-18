package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newAgentTestServer wires a server over a config the health/probe tests control:
// every project admits codex, the store is returned so a test can seed job history,
// and "codex" is a cli-agent running the test binary (a real child process whose
// argv the probe can execute).
func newAgentTestServer(t *testing.T, agents map[string]config.AgentConfig, projects map[string]config.ProjectConfig) (*Server, *jobstore.Store) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:   config.ServerConfig{Token: testToken},
		Storage:  config.StorageConfig{Root: root},
		Agents:   agents,
		Projects: projects,
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	st := openTestStore(t, root)
	jobs := job.NewService(cfg, projReg, agentReg, runners, st, nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projReg, agentReg, nil, nil, nil, nil), st
}

// seedFailed writes one terminal failed job row for an agent, so the health aggregate
// has evidence.
func seedFailed(t *testing.T, st *jobstore.Store, agentKey, class string, at int64) {
	t.Helper()
	rec := jobstore.JobRecord{
		ID:           "seed-" + agentKey + "-" + class + "-" + string(rune('a'+at%26)),
		ProjectKey:   "alpha",
		Agent:        agentKey,
		Runner:       "local",
		Status:       "failed",
		FailureClass: class,
		StartedAt:    at,
		EndedAt:      at + 5,
		UpdatedAt:    at + 5,
	}
	if err := st.UpsertJob(rec); err != nil {
		t.Fatalf("seed job: %v", err)
	}
}

// TestListAgentsIncludesHealth: GET /v1/agents carries each agent's recent-job health
// so the console (and `gofer agent status`) can show a degraded provider without a
// second round of queries; an agent with no recent job is explicitly "unknown" rather
// than silently healthy.
func TestListAgentsIncludesHealth(t *testing.T) {
	bin := testcmd.Path(t)
	agents := map[string]config.AgentConfig{
		"codex": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", "OK"}},
		"omp":   {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"printf", "OK"}},
	}
	projects := map[string]config.ProjectConfig{
		"alpha": {HostPath: t.TempDir(), AllowedAgents: []string{"codex", "omp"}, AllowedRunners: []string{"local"}},
	}
	s, st := newAgentTestServer(t, agents, projects)
	now := time.Now().Unix()
	for i := range 3 {
		seedFailed(t, st, "codex", job.FailureClassTransient, now-100+int64(i))
	}

	resp := do(t, s, http.MethodGet, "/v1/agents", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Agents []agentView `json:"agents"`
	}
	decode(t, resp, &body)
	views := map[string]agentView{}
	for _, a := range body.Agents {
		views[a.Key] = a
	}

	codex, ok := views["codex"]
	if !ok || codex.Health == nil {
		t.Fatalf("codex view = %+v, want a health block", codex)
	}
	if codex.Health.State != agent.HealthDegraded {
		t.Fatalf("codex health = %+v, want degraded after 3 provider errors", codex.Health)
	}
	if codex.Health.WindowSec != config.DefaultAgentHealthWindowSec || codex.Health.Jobs != 3 || codex.Health.TransientFail != 3 || codex.Health.OK != 0 {
		t.Fatalf("codex health counts = %+v, want 3 jobs / 0 ok / 3 transient in a %ds window", codex.Health, config.DefaultAgentHealthWindowSec)
	}
	if codex.Health.LastTransientAt == 0 {
		t.Fatalf("codex health must report when the outage happened: %+v", codex.Health)
	}

	if got := views["omp"]; got.Health == nil || got.Health.State != agent.HealthUnknown {
		t.Fatalf("omp health = %+v, want unknown (no recent job)", got.Health)
	}
}
