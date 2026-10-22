package job

import (
	"strings"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
)

// The gofer mailbox's resident 收件会话 (design 2026-10-22 mail-agent-address「D4」) is an
// ordinary continuous ACP session job with two server-stamped extras: the gofer MCP server
// injected into its session (so the mailbox tools are there without an operator wiring
// them up), and the GOFER_MAIL_RESIDENT marker, which the acp approval gate reads to
// auto-allow ONE tool — gofer_session_mail_reply, the answer path of a letter
// (gofer-sh7r). Everything else the session does stays under its approval policy.

// goferMailResidentEnv is the marker a resident mailbox session's process carries. It
// narrows nothing by itself: the MCP child only needs the job credential.
const goferMailResidentEnv = "GOFER_MAIL_RESIDENT"

// mailResidentMCPServer builds the ACP mcpServers entry a resident mailbox session gets:
// this server's own gofer binary running `mcp` with the job's credential, so the session
// can answer letters (gofer-sh7r).
//
// ok=false when no credential was minted: the session then runs without a gofer MCP
// instead of with a half-working one (the start is not refused — a mailbox without
// tools is still a working session, and the failure is visible in its logs).
func mailResidentMCPServer(env map[string]string) (runner.ACPMCPServer, bool) {
	return goferMCPStdioServer(env, map[string]string{goferMailResidentEnv: "1"})
}

// applyMailResidentRun stamps a resident mailbox session's run request: the marker in its
// env and the gofer MCP in its ACP session. A same-named server the agent definition
// lists is replaced, so the session's gofer is always the one carrying its credential.
func applyMailResidentRun(runReq *runner.Request) {
	if runReq.ACP == nil {
		return
	}
	runReq.Env = util.EnvWith(runReq.Env, map[string]string{goferMailResidentEnv: "1"})
	srv, ok := mailResidentMCPServer(runReq.Env)
	if !ok {
		return
	}
	kept := runReq.ACP.MCPServers[:0:0]
	for _, m := range runReq.ACP.MCPServers {
		if m.Name != srv.Name {
			kept = append(kept, m)
		}
	}
	runReq.ACP.MCPServers = append(kept, srv)
	runReq.ACP.MailResident = strings.TrimSpace(runReq.Env[EnvJobToken]) != ""
}
