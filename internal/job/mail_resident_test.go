package job

import (
	"testing"

	"github.com/inhere/gofer/internal/runner"
)

// TestMailResidentRunGetsTheGoferMCP is the gofer-sh7r rule: a resident 收件会话's ACP
// session is advertised gofer's own MCP server (built from the job's credential, so the
// mailbox tools work without an operator wiring them up) and its run carries the marker
// the approval gate auto-allows a letter's answer on.
func TestMailResidentRunGetsTheGoferMCP(t *testing.T) {
	runReq := runner.Request{
		Env: map[string]string{EnvJobToken: "tok-1", EnvJobBin: `C:\gofer\gofer.exe`, EnvServerAddr: "http://127.0.0.1:7777"},
		// The agent definition lists a same-named server without a credential: it must be
		// replaced, not kept next to ours.
		ACP: &runner.ACPRequest{MCPServers: []runner.ACPMCPServer{
			{Name: stewardMCPName, Command: "stale"},
			{Name: "db", Command: "db-mcp"},
		}},
	}
	applyMailResidentRun(&runReq)

	if runReq.Env[goferMailResidentEnv] != "1" {
		t.Fatalf("the resident marker is missing from the run env: %v", runReq.Env)
	}
	if !runReq.ACP.MailResident {
		t.Fatal("the run request must be marked as a resident mailbox session")
	}
	var gofer []runner.ACPMCPServer
	var db bool
	for _, s := range runReq.ACP.MCPServers {
		switch s.Name {
		case stewardMCPName:
			gofer = append(gofer, s)
		case "db":
			db = true
		}
	}
	if len(gofer) != 1 {
		t.Fatalf("gofer MCP entries = %d, want exactly the injected one: %+v", len(gofer), runReq.ACP.MCPServers)
	}
	srv := gofer[0]
	if srv.Command != `C:\gofer\gofer.exe` {
		t.Fatalf("command = %q, want the job's own gofer binary", srv.Command)
	}
	if len(srv.Args) != 1 || srv.Args[0] != "mcp" {
		t.Fatalf("args = %v, want [mcp]", srv.Args)
	}
	if srv.Env[EnvJobToken] != "tok-1" {
		t.Fatalf("the MCP child did not get the job credential: %v", srv.Env)
	}
	if srv.Env[EnvServerAddr] != "http://127.0.0.1:7777" {
		t.Fatalf("the MCP child did not get the hub address: %v", srv.Env)
	}
	if srv.Env[goferMailResidentEnv] != "1" {
		t.Fatalf("the MCP child did not get the resident marker: %v", srv.Env)
	}
	if srv.Env[goferStewardEnv] == "1" {
		// A mailbox session is not a steward: the steward marker narrows the MCP to the
		// steward tool list, which would hide the mailbox tools.
		t.Fatalf("a resident mailbox session must not carry the steward marker: %v", srv.Env)
	}
	if !db {
		t.Fatal("the agent's own MCP servers must be kept")
	}
}

// TestMailResidentRunWithoutACredentialInjectsNothing: with no job credential there is no
// usable gofer MCP, so the session starts without one rather than with a half-working one,
// and the gate allowance is not switched on.
func TestMailResidentRunWithoutACredentialInjectsNothing(t *testing.T) {
	runReq := runner.Request{Env: map[string]string{}, ACP: &runner.ACPRequest{}}
	applyMailResidentRun(&runReq)
	if len(runReq.ACP.MCPServers) != 0 {
		t.Fatalf("injected %+v without a credential", runReq.ACP.MCPServers)
	}
	if runReq.ACP.MailResident {
		t.Fatal("the reply allowance must not be switched on without a gofer MCP")
	}
}
