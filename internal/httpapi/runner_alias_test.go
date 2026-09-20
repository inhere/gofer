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

// newAliasServer builds a Server whose "self" project allows the built-in runner
// under the allowlist SPELLING a test chooses, so the allowlist check is exercised
// at the HTTP boundary rather than only inside the job package.
func newAliasServer(t *testing.T, token string, allowed []string) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: token},
		Storage: config.StorageConfig{Root: root},
		Agents: map[string]config.AgentConfig{
			"echoagent": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}},
		},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec", "echoagent"},
				AllowedRunners: allowed,
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, token, false, jobs, eng, projects, agents, nil, nil, nil, nil)
}

// TestSubmitAcceptsBuiltinRunnerAliasOverHTTP is the boundary a non-CLI caller
// actually uses: POST /v1/jobs with the runner spelled the way the CLI documents
// it (`server`). It must be accepted and STORED under the canonical key, so the
// job list / web console / /v1/runners keep one vocabulary.
//
// Regression: this returned 400 `runner "server" is not allowed in project`, since
// only the CLI translated the alias before sending.
func TestSubmitAcceptsBuiltinRunnerAliasOverHTTP(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: config.BuiltinLocalRunnerAlias,
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, Sync: true,
	})
	if resp.StatusCode != http.StatusOK {
		var eb errorBody
		decode(t, resp, &eb)
		t.Fatalf("status=%d (err=%s), want 200 for the built-in runner's alias", resp.StatusCode, eb.Error)
	}
	var jr job.JobResult
	decode(t, resp, &jr)
	if jr.Status != job.StatusDone {
		t.Fatalf("status=%s (err=%s), want done", jr.Status, jr.Error)
	}
	if jr.Runner != config.BuiltinLocalRunner {
		t.Fatalf("runner=%q, want the canonical %q", jr.Runner, config.BuiltinLocalRunner)
	}
}

// TestMarkdownSubmitAcceptsBuiltinRunnerAlias: the `-f task.md` path sends the file
// to the server, which parses the frontmatter into a JobRequest — so a task file
// written with the documented spelling must not be the one submit path that fails.
func TestMarkdownSubmitAcceptsBuiltinRunnerAlias(t *testing.T) {
	s := newAliasServer(t, testToken, []string{config.BuiltinLocalRunner})
	md := []byte("---\nproject_key: self\nagent: echoagent\nrunner: server\n---\nsay hi\n")
	resp := doRaw(t, s, http.MethodPost, "/v1/jobs", testToken, "text/markdown", md)
	if resp.StatusCode != http.StatusOK {
		var eb errorBody
		decode(t, resp, &eb)
		t.Fatalf("status=%d (err=%s), want 200 (md submit accepted)", resp.StatusCode, eb.Error)
	}
	var jr job.JobResult
	decode(t, resp, &jr)
	if jr.Runner != config.BuiltinLocalRunner {
		t.Fatalf("runner=%q, want the canonical %q", jr.Runner, config.BuiltinLocalRunner)
	}
}

// TestSubmitAcceptsAliasSpelledAllowlist: `allowed_runners: [server]` is what an
// operator writes from the CLI help, and it must admit the built-in runner — under
// either spelling, because they name one runner.
func TestSubmitAcceptsAliasSpelledAllowlist(t *testing.T) {
	s := newAliasServer(t, testToken, []string{config.BuiltinLocalRunnerAlias})
	for _, spelling := range []string{config.BuiltinLocalRunnerAlias, config.BuiltinLocalRunner} {
		resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: spelling,
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30, Sync: true,
		})
		if resp.StatusCode != http.StatusOK {
			var eb errorBody
			decode(t, resp, &eb)
			t.Fatalf("runner %q: status=%d (err=%s), want 200", spelling, resp.StatusCode, eb.Error)
		}
		var jr job.JobResult
		decode(t, resp, &jr)
		if jr.Status != job.StatusDone {
			t.Fatalf("runner %q: status=%s (err=%s), want done", spelling, jr.Status, jr.Error)
		}
	}
}

// TestRunnerListKeepsCanonicalName: the alias is an INPUT spelling only. The runner
// roster (and with it the web console's picker) must keep reporting the one
// canonical row, so accepting "server" never grows a phantom second runner.
func TestRunnerListKeepsCanonicalName(t *testing.T) {
	s := newAliasServer(t, testToken, []string{config.BuiltinLocalRunnerAlias})
	resp := do(t, s, http.MethodGet, "/v1/runners", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Runners []struct {
			Name string `json:"name"`
		} `json:"runners"`
	}
	decode(t, resp, &out)
	if len(out.Runners) != 1 || out.Runners[0].Name != config.BuiltinLocalRunner {
		t.Fatalf("runners = %+v, want exactly the canonical %q row", out.Runners, config.BuiltinLocalRunner)
	}
}
