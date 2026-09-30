package httpapi

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func newACPSessionServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"acpbot"}, AllowedRunners: []string{"local"},
		}},
		Agents: map[string]config.AgentConfig{"acpbot": {
			Type: agent.TypeACPAgent, Command: testcmd.Path(t), Args: acptest.CmdArgs(acptest.Options{}),
		}},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New(), acprunner.Name: acprunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil)
}

func waitACPSession(t *testing.T, s *Server, id string, turn int) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		result, ok := s.jobs.Get(id)
		if ok && result.Status == job.StatusAwaitingInput && result.TurnNo == turn {
			return result
		}
		if ok && job.IsTerminal(result.Status) {
			t.Fatalf("session ended early: %+v", result)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s did not await turn %d", id, turn)
	return job.JobResult{}
}

func TestSessionJobHTTPSTurnAndEnd(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
	})
	var result job.JobResult
	decode(t, created, &result)
	_ = waitDone(t, s, result.ID)
	for _, action := range []string{"say", "end"} {
		resp := do(t, s, http.MethodPost, "/v1/jobs/"+result.ID+"/"+action, testToken, map[string]string{"message": "next"})
		if resp.StatusCode != http.StatusConflict {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s on non-session job status=%d body=%s, want 409", action, resp.StatusCode, body)
		}
		_ = resp.Body.Close()
	}
	acpServer := newACPSessionServer(t)
	resp := do(t, acpServer, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Prompt: "first", Session: true,
		TimeoutSec: 20, IdleTimeoutSec: 20,
	})
	var session job.JobResult
	decode(t, resp, &session)
	waitACPSession(t, acpServer, session.ID, 1)
	say := do(t, acpServer, http.MethodPost, "/v1/jobs/"+session.ID+"/say", testToken, map[string]string{"message": "second"})
	if say.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(say.Body)
		t.Fatalf("say status=%d body=%s", say.StatusCode, body)
	}
	_ = say.Body.Close()
	waitACPSession(t, acpServer, session.ID, 2)
	end := do(t, acpServer, http.MethodPost, "/v1/jobs/"+session.ID+"/end", testToken, nil)
	if end.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(end.Body)
		t.Fatalf("end status=%d body=%s", end.StatusCode, body)
	}
	_ = end.Body.Close()
	final, ok := acpServer.jobs.Wait(session.ID)
	if !ok || final.Status != job.StatusDone || final.ID != session.ID {
		t.Fatalf("HTTP session final: ok=%v result=%+v", ok, final)
	}
}

func TestSessionJobSayEndCallerScope(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs/no-such-session/say", testToken, map[string]string{"message": "next"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session say status=%d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
	acpServer := newACPSessionServer(t)
	created, err := acpServer.jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Prompt: "first", Session: true,
		TimeoutSec: 20, IdleTimeoutSec: 20, CallerID: "parent-job",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitACPSession(t, acpServer, created.ID, 1)
	ownerToken := seedJobToken(t, acpServer, "parent-job", jobstore.JobCredentialMember, "")
	otherToken := seedJobToken(t, acpServer, "other-job", jobstore.JobCredentialMember, "")
	denied := do(t, acpServer, http.MethodPost, "/v1/jobs/"+created.ID+"/say", otherToken, map[string]string{"message": "bad"})
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("other job say status=%d, want 403", denied.StatusCode)
	}
	_ = denied.Body.Close()
	allowed := do(t, acpServer, http.MethodPost, "/v1/jobs/"+created.ID+"/say", ownerToken, map[string]string{"message": "second"})
	if allowed.StatusCode != http.StatusOK {
		t.Fatalf("owner job say status=%d, want 200", allowed.StatusCode)
	}
	_ = allowed.Body.Close()
	waitACPSession(t, acpServer, created.ID, 2)
	ended := do(t, acpServer, http.MethodPost, "/v1/jobs/"+created.ID+"/end", testToken, nil)
	if ended.StatusCode != http.StatusOK {
		t.Fatalf("user caller end status=%d, want 200", ended.StatusCode)
	}
	_ = ended.Body.Close()
}
