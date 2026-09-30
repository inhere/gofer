package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newAssignableServer builds a server with two registered workers (w1/w2, each with
// its own token) and four projects, so the endpoint's filtering can be asserted in
// both directions: what w1 may run (directly listed runner key, and via a runner
// pinned to w1) versus what it may not (a local-only project and w2's project).
func newAssignableServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	host := func(name string) string { return root + "/" + name }
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token: testToken,
			Workers: map[string]config.WorkerAuthConfig{
				"w1": {Token: "tok-w1"},
				"w2": {Token: "tok-w2"},
			},
		},
		Runners: map[string]config.RunnerConfig{
			"w1":      {Type: "worker", WorkerID: "w1"},
			"builder": {Type: "worker", WorkerID: "w1"},
			"w2":      {Type: "worker", WorkerID: "w2"},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			// The literal form: the worker id IS the runner key in allowed_runners.
			"p-direct": {HostPath: host("p-direct"), AllowedAgents: []string{"exec"}, AllowedRunners: []string{"w1"}},
			// The delegated form: allowed_runners names a runner pinned to w1.
			"p-runner": {HostPath: host("p-runner"), AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local", "builder"}},
			// Belongs to another worker / to nobody.
			"p-other": {HostPath: host("p-other"), AllowedAgents: []string{"exec"}, AllowedRunners: []string{"w2"}},
			"p-local": {HostPath: host("p-local"), AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	s := New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, cfg.Runners, nil, nil)
	s.SetBuildInfo(buildinfo.Info{Version: "v0.99"})
	return s
}

// TestAssignableEndpoint pins GET /v1/workers/{id}/assignable: the projects whose
// allowed_runners can dispatch to this worker (the id itself, or a runner pinned to
// it), plus the server/protocol version — and the auth rule: a user caller and the
// worker's OWN token may read it, another worker's token may not.
func TestAssignableEndpoint(t *testing.T) {
	t.Parallel()
	s := newAssignableServer(t)

	read := func(t *testing.T, token string) (*http.Response, workerAssignableResp) {
		t.Helper()
		resp := do(t, s, http.MethodGet, "/v1/workers/w1/assignable", token, nil)
		var body workerAssignableResp
		if resp.StatusCode == http.StatusOK {
			decode(t, resp, &body)
		}
		return resp, body
	}

	// A user caller reads it.
	resp, body := read(t, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("user caller status=%d, want 200", resp.StatusCode)
	}
	if body.WorkerID != "w1" {
		t.Fatalf("worker_id=%q, want w1", body.WorkerID)
	}
	if body.ServerVersion == "" || body.ProtocolVersion == 0 {
		t.Fatalf("server_version=%q protocol_version=%d, want both reported",
			body.ServerVersion, body.ProtocolVersion)
	}
	got := map[string]string{}
	for _, p := range body.Projects {
		got[p.Key] = p.HostPath
	}
	if len(got) != 2 {
		t.Fatalf("projects=%v, want exactly the two assignable to w1", body.Projects)
	}
	for _, key := range []string{"p-direct", "p-runner"} {
		if got[key] == "" {
			t.Fatalf("project %q missing from %v (host_path must be carried)", key, body.Projects)
		}
	}
	if _, ok := got["p-other"]; ok {
		t.Fatalf("p-other (allowed_runners=[w2]) must not be assignable to w1: %v", body.Projects)
	}
	if _, ok := got["p-local"]; ok {
		t.Fatalf("p-local (allowed_runners=[local]) must not be assignable to w1: %v", body.Projects)
	}

	// The worker's own token reads it.
	if resp, _ := read(t, "tok-w1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("worker w1's own token status=%d, want 200", resp.StatusCode)
	}
	// Another worker's token is refused.
	if resp, _ := read(t, "tok-w2"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker w2's token status=%d, want 403", resp.StatusCode)
	}
}
