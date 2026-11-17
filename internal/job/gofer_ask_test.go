package job

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
)

func TestWantsGoferAskOnlyForResidentSessions(t *testing.T) {
	cases := []struct {
		name string
		req  JobRequest
		want bool
	}{
		{"resident session", JobRequest{Session: true}, true},
		{"one-shot job", JobRequest{}, false},
		{"steward session", JobRequest{Session: true, Steward: true}, false},
		{"mailbox session", JobRequest{Session: true, MailResident: true}, false},
		{"leader round", JobRequest{Session: true, LeaderOfPlan: "p1"}, false},
	}
	for _, c := range cases {
		if got := wantsGoferAsk(c.req); got != c.want {
			t.Errorf("%s: wantsGoferAsk = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestApplyGoferAskRunInjectsTheNarrowGoferMCP(t *testing.T) {
	runReq := runner.Request{
		Env: map[string]string{EnvJobToken: "tok-1", EnvJobBin: "/opt/gofer", EnvServerAddr: "http://127.0.0.1:7777", "GOFER_JOB_ID": "j1"},
		ACP: &runner.ACPRequest{MCPServers: []runner.ACPMCPServer{{Name: stewardMCPName, Command: "stale"}, {Name: "db", Command: "db-mcp"}}},
	}
	applyGoferAskRun(&runReq)

	if runReq.Env[goferAskEnv] != "1" {
		t.Fatalf("GOFER_ASK missing from the run env: %v", runReq.Env)
	}
	var gofer []runner.ACPMCPServer
	var db bool
	for _, m := range runReq.ACP.MCPServers {
		switch m.Name {
		case stewardMCPName:
			gofer = append(gofer, m)
		case "db":
			db = true
		}
	}
	if len(gofer) != 1 || !db {
		t.Fatalf("want exactly the injected gofer MCP next to the agent's own: %+v", runReq.ACP.MCPServers)
	}
	srv := gofer[0]
	if srv.Command != "/opt/gofer" || srv.Env[EnvJobToken] != "tok-1" || srv.Env[EnvServerAddr] != "http://127.0.0.1:7777" ||
		srv.Env["GOFER_JOB_ID"] != "j1" || srv.Env[goferAskEnv] != "1" {
		t.Fatalf("injected MCP is wrong: %+v", srv)
	}
	if srv.Env[goferStewardEnv] != "" || srv.Env[goferMailResidentEnv] != "" {
		t.Fatalf("a gofer_ask MCP must carry no steward / mailbox marker: %v", srv.Env)
	}
}

func TestApplyGoferAskRunWithoutACredentialInjectsNothing(t *testing.T) {
	runReq := runner.Request{Env: map[string]string{}, ACP: &runner.ACPRequest{}}
	applyGoferAskRun(&runReq)
	if len(runReq.ACP.MCPServers) != 0 || runReq.Env[goferAskEnv] != "" {
		t.Fatalf("no credential, nothing to inject: %+v env=%v", runReq.ACP.MCPServers, runReq.Env)
	}
}

// A resident ACP session is advertised the gofer_ask MCP; a one-shot ACP job is not (it is
// unattended: a question would park it until its timeout).
func TestSessionJobGetsGoferAskOneShotDoesNot(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{EnvPrint: []string{EnvJobToken, goferAskEnv, "GOFER_STEWARD"}})

	sess := stewardSession(t, s, false)
	log := readJobLog(t, sess, store.StderrFile)
	if !strings.Contains(log, "mcp_servers=1") || envLine(log, goferAskEnv) != "1" {
		t.Fatalf("a resident session must be advertised the gofer_ask MCP: %s", log)
	}
	if envLine(log, "GOFER_STEWARD") != "" {
		t.Fatalf("a gofer_ask session is not a steward: %s", log)
	}
	_ = s.EndSession(sess.ID)
	s.Wait(sess.ID)

	one, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "hello", TimeoutSec: 20})
	if err != nil {
		t.Fatal(err)
	}
	s.Wait(one.ID)
	res, _ := s.Get(one.ID)
	olog := readJobLog(t, res, store.StderrFile)
	if !strings.Contains(olog, "mcp_servers=0") || envLine(olog, goferAskEnv) != "" {
		t.Fatalf("a one-shot ACP job must get no gofer_ask MCP: %s", olog)
	}
}

// A worker re-entry (CredentialExternal) runs a LOCAL job with its own id, but carries the
// hub job's credential: GOFER_JOB_ID, and with it the gofer MCP's job id, must be the hub
// job's, never the local one (the credential only reaches its own job).
func TestWorkerReentryUsesTheHubJobIDForTheJobEnv(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{EnvPrint: []string{EnvJobToken, "GOFER_JOB_ID", goferAskEnv}})
	const hubID = "hub-job-20261117-000000-abcd1234"
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "hello", Session: true, TimeoutSec: 20, IdleTimeoutSec: 20,
		JobToken: "gjt_" + hubID + "_0123456789abcdef", CredentialExternal: true, CredentialJobID: hubID,
		Env: map[string]string{EnvServerAddr: "http://hub.example:8765"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionTurn(t, s, res.ID, 1)
	if res.ID == hubID {
		t.Fatal("the local job must have its own id")
	}
	log := readJobLog(t, res, store.StderrFile)
	if got := envLine(log, "GOFER_JOB_ID"); got != hubID {
		t.Fatalf("GOFER_JOB_ID = %q, want the hub job id %q (local id %s)\n%s", got, hubID, res.ID, log)
	}
	if envLine(log, goferAskEnv) != "1" || !strings.Contains(log, "mcp_servers=1") {
		t.Fatalf("the session must still get the gofer_ask MCP: %s", log)
	}
	_ = s.EndSession(res.ID)
	s.Wait(res.ID)
}

// The MCP entry built for a session takes its job id from the run env, so the hub id above
// is what the gofer_ask child is started with.
func TestGoferAskMCPCarriesTheEnvJobID(t *testing.T) {
	runReq := runner.Request{
		Env: map[string]string{EnvJobToken: "tok", EnvJobBin: "/opt/gofer", "GOFER_JOB_ID": "hub-1"},
		ACP: &runner.ACPRequest{},
	}
	applyGoferAskRun(&runReq)
	if len(runReq.ACP.MCPServers) != 1 || runReq.ACP.MCPServers[0].Env["GOFER_JOB_ID"] != "hub-1" {
		t.Fatalf("MCP entry = %+v, want GOFER_JOB_ID=hub-1", runReq.ACP.MCPServers)
	}
}
