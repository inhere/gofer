package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
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

// deliverE2EServer builds a real job-service-backed server with one self-built cli-agent
// whose deliver_command is the test helper's fake-deliver (exit code + record file).
func deliverE2EServer(t *testing.T, exit string, stdin bool) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	rec := filepath.Join(root, "rec.txt")
	dc := []string{"fake-deliver", exit, rec, "--session", "{{session_id}}"}
	if !stdin {
		dc = append(dc, "--text", "{{text}}")
	}
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"myagent"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{"myagent": {
			Type: agent.TypeCLIAgent, Command: testcmd.Path(t), DeliverCommand: dc, DeliverStdin: stdin,
		}},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil), rec
}

func registerMyagent(t *testing.T, s *Server, sid string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": sid, "agent": "myagent", "runner": "server", "cwd": s.projects.Config().Projects["self"].HostPath,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status=%d", resp.StatusCode)
	}
	resp.Body.Close()
}

// The deliver endpoint runs the agent's deliver_command as a real internal job on the
// session's runner: argv carries the session id (and the text, or stdin does), exit 0 is
// delivered (path=command, state running), exit 3 falls down the ladder (no tmux here, so
// 409 no_tmux), any other exit is 502 deliver_failed with the stderr as the reason.
func TestSessionDeliverCommandEndToEnd(t *testing.T) {
	t.Parallel()
	for _, stdin := range []bool{false, true} {
		s, rec := deliverE2EServer(t, "0", stdin)
		registerMyagent(t, s, "sid-cmd-e2e")
		resp := do(t, s, http.MethodPost, "/v1/sessions/sid-cmd-e2e/deliver", testToken, map[string]any{"text": "hi 你好 \"q\" $x"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stdin=%v deliver status=%d", stdin, resp.StatusCode)
		}
		var out struct {
			Path  string `json:"path"`
			JobID string `json:"job_id"`
		}
		decode(t, resp, &out)
		if out.Path != "command" || out.JobID == "" {
			t.Fatalf("result = %+v", out)
		}
		b, err := os.ReadFile(rec)
		if err != nil {
			t.Fatalf("the deliver command did not run: %v", err)
		}
		got := string(b)
		wantText := "[gofer web 回复] hi 你好 \"q\" $x"
		if stdin {
			if !strings.Contains(got, "stdin="+wantText) || strings.Contains(strings.SplitN(got, "\n", 2)[0], "hi") {
				t.Fatalf("stdin form record = %q", got)
			}
		} else if !strings.Contains(got, "--text\x1f"+wantText) || !strings.Contains(got, "stdin=") {
			t.Fatalf("argv form record = %q", got)
		}
		if !strings.Contains(got, "--session\x1fsid-cmd-e2e") {
			t.Fatalf("session id missing from argv: %q", got)
		}
	}

	s, _ := deliverE2EServer(t, "3", true)
	registerMyagent(t, s, "sid-cmd-nr")
	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-cmd-nr/deliver", testToken, map[string]any{"text": "x"})
	var env struct {
		Error string `json:"error"`
	}
	decode(t, resp, &env)
	if resp.StatusCode != http.StatusConflict || env.Error != "deliver failed: no_tmux" {
		t.Fatalf("not-running status=%d error=%q, want 409 no_tmux (ladder continues)", resp.StatusCode, env.Error)
	}

	s, _ = deliverE2EServer(t, "1", false)
	registerMyagent(t, s, "sid-cmd-bad")
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-cmd-bad/deliver", testToken, map[string]any{"text": "x"})
	decode(t, resp, &env)
	if resp.StatusCode != http.StatusBadGateway || !strings.HasPrefix(env.Error, "deliver failed: deliver_failed:fake deliver failed") {
		t.Fatalf("failed status=%d error=%q, want 502 deliver_failed", resp.StatusCode, env.Error)
	}
}

func TestDeliverPlanRendering(t *testing.T) {
	t.Parallel()
	s, _ := deliverE2EServer(t, "0", false)
	p := s.deliverPlan("myagent", "sid-1", "T")
	if len(p.Argv) < 2 || p.Argv[1] != "fake-deliver" || p.Stdin != "" || p.Argv[len(p.Argv)-1] != "T" {
		t.Fatalf("argv plan = %+v", p)
	}
	if got := s.deliverPlan("nope", "sid-1", "T"); len(got.Argv) != 0 {
		t.Fatalf("unknown agent plan = %+v", got)
	}
}
