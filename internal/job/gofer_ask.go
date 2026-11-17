package job

import (
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
)

// goferAskEnv marks the process of a resident ACP session whose gofer MCP carries only
// the gofer_ask tool (see mcpserver.goferAskMode).
const goferAskEnv = "GOFER_ASK"

// wantsGoferAsk reports whether a job's ACP session gets the gofer_ask MCP: only a
// resident (--session) ACP job, where somebody is expected to be watching. A one-shot or
// batch job is unattended — a question there would park it until its timeout — and the
// steward, the resident mailbox session and a leader round have their own gofer surface
// (the first two register gofer_ask there themselves; a leader round is unattended and has
// gofer_ask_human for plan decisions).
func wantsGoferAsk(req JobRequest) bool {
	return req.Session && !req.Steward && !req.MailResident && req.LeaderOfPlan == ""
}

// applyGoferAskRun stamps a resident session's run request: the gofer_ask marker in its env
// and the gofer MCP (job credential, hub address, job id) in its ACP session. A same-named
// server the agent's own config lists is replaced, so the session's gofer is always the
// one carrying its credential. Without a minted credential nothing is injected — the same
// rule as the steward's and the mailbox's MCP.
func applyGoferAskRun(runReq *runner.Request) {
	if runReq.ACP == nil {
		return
	}
	srv, ok := goferMCPStdioServer(runReq.Env, map[string]string{goferAskEnv: "1"})
	if !ok {
		return
	}
	runReq.Env = util.EnvWith(runReq.Env, map[string]string{goferAskEnv: "1"})
	kept := runReq.ACP.MCPServers[:0:0]
	for _, m := range runReq.ACP.MCPServers {
		if m.Name != srv.Name {
			kept = append(kept, m)
		}
	}
	runReq.ACP.MCPServers = append(kept, srv)
}
