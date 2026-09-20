package httpapi

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/runner/peerhttp"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// acpBridge is one node (hub or peer) of the peer fixture: a wired job.Service plus
// the HTTP server its peer counterpart talks to.
type acpBridge struct {
	jobs *job.Service
	srv  *httptest.Server
}

// newACPPeerBridge starts the PEER node: project "demo" runs the repo's fake ACP
// agent through the testcmd binary, with the acp runner registered under its own key
// (what core does in production) so the peer really speaks the protocol.
func newACPPeerBridge(t *testing.T) *acpBridge {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{AllowEmptyToken: true},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"demo": {
				HostPath:       root,
				AllowedAgents:  []string{"acpbot"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"acpbot": {Type: agent.TypeACPAgent, Command: testcmd.Path(t), Args: acptest.CmdArgs(acptest.Options{})},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		acprunner.Name:   acprunner.New(),
	}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, "", true, jobs, eng, projects, agents, nil, nil, nil, nil)
	return &acpBridge{jobs: jobs, srv: httptest.NewServer(s.Handler())}
}

// newACPHostBridge starts the HUB node: its project routes "acpbot" through a
// peer-http runner, so the job executes on the peer. The agent key is defined on the
// hub too — not to run it, but because ResumeJob reads the SOURCE agent's ACP policy
// (whether a session may be loaded at all) before forwarding the continuation.
func newACPHostBridge(t *testing.T, peerURL string) *acpBridge {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{AllowEmptyToken: true},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"demo": {
				HostPath:       root,
				AllowedAgents:  []string{"acpbot"},
				AllowedRunners: []string{"peer-stub"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"acpbot": {Type: agent.TypeACPAgent, Command: testcmd.Path(t), Args: acptest.CmdArgs(acptest.Options{})},
		},
		Runners: map[string]config.RunnerConfig{
			"peer-stub": {Type: "peer-http", BaseURL: peerURL},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		"peer-stub":      peerhttp.New("peer-stub", peerURL, ""),
	}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, "", true, jobs, eng, projects, agents, nil, nil, nil, nil)
	return &acpBridge{jobs: jobs, srv: httptest.NewServer(s.Handler())}
}

// waitJobTerminal polls one node's in-process job service until the job is terminal.
func waitJobTerminal(t *testing.T, jobs *job.Service, id string, timeout time.Duration) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if jr, ok := jobs.Get(id); ok && job.IsTerminal(jr.Status) {
			return jr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal state within %s", id, timeout)
	return job.JobResult{}
}

// waitPeerContinuation finds the peer's job that continues the hub's source job:
// its row carries the lineage the hub forwarded (resumed_from = the source job id).
func waitPeerContinuation(t *testing.T, jobs *job.Service, srcJobID string, timeout time.Duration) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		listed, err := jobs.ListJobs(job.ListOpts{})
		if err != nil {
			t.Fatalf("list peer jobs: %v", err)
		}
		for _, pj := range listed {
			if pj.ResumedFrom == srcJobID && job.IsTerminal(pj.Status) {
				return pj
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the peer never ran a continuation of %s", srcJobID)
	return job.JobResult{}
}

// TestPeerACPResumeLoadsSession is the end-to-end proof of h-aii-9qiy / SUP-02 R1:
// an acp-agent job runs on a PEER; resuming it on the hub carries the continuation
// across the hop (session_id + resumed_from), so the peer's agent is asked to
// session/LOAD the source session instead of opening a fresh one — the behaviour the
// dropped lineage field used to cost.
func TestPeerACPResumeLoadsSession(t *testing.T) {
	peer := newACPPeerBridge(t)
	defer peer.srv.Close()
	host := newACPHostBridge(t, peer.srv.URL)
	defer host.srv.Close()

	src, err := host.jobs.Submit(job.JobRequest{
		ProjectKey: "demo", Agent: "acpbot", Runner: "peer-stub",
		Prompt: "list the files in this directory", Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("host submit: %v", err)
	}
	srcFinal := waitJobTerminal(t, host.jobs, src.ID, 30*time.Second)
	if srcFinal.Status != job.StatusDone {
		t.Fatalf("source status = %s (err=%s), want done", srcFinal.Status, srcFinal.Error)
	}
	if srcFinal.SessionID != acptest.SessionID {
		t.Fatalf("source session_id = %q, want the peer agent's %q", srcFinal.SessionID, acptest.SessionID)
	}

	resumed, err := host.jobs.ResumeJob(src.ID, "keep going", "", "caller-1")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	resFinal := waitJobTerminal(t, host.jobs, resumed.ID, 30*time.Second)
	if resFinal.Status != job.StatusDone {
		t.Fatalf("resumed status = %s (err=%s), want done", resFinal.Status, resFinal.Error)
	}
	if resFinal.SessionID != acptest.SessionID {
		t.Fatalf("resumed session_id = %q, want %q", resFinal.SessionID, acptest.SessionID)
	}

	// The peer ran it AS a continuation (the forwarded lineage is on the peer's row).
	peerJob := waitPeerContinuation(t, peer.jobs, src.ID, 30*time.Second)
	agentLog, err := os.ReadFile(filepath.Join(peerJob.ResultDir, store.StderrFile))
	if err != nil {
		t.Fatalf("read the peer job's stderr: %v", err)
	}
	if !strings.Contains(string(agentLog), "acptest: session/load sid="+acptest.SessionID) {
		t.Fatalf("the peer's agent was not asked to load session %s:\n%s", acptest.SessionID, agentLog)
	}
	if strings.Contains(string(agentLog), "acptest: session/new") {
		t.Fatalf("a resumed peer job must not open a new session:\n%s", agentLog)
	}
}
