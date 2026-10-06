package job

import (
	"fmt"
	"os"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
)

// W2b steward plumbing: the steward is an ordinary resident ACP session job with three
// server-stamped extras — the `steward` credential kind (see credential.go), a gofer MCP
// server injected into its ACP session, and the GOFER_STEWARD environment marker that
// narrows that MCP to the steward tool whitelist. Nothing here trusts the agent: the
// credential (checked by the HTTP layer, default-deny) is the real boundary, the MCP
// narrowing only keeps the tool list honest.

// stewardMCPName is the name the injected MCP server is advertised under.
const stewardMCPName = "gofer"

// StewardTag labels the steward's session job (Sessions and the work page show it).
const StewardTag = "steward"

// stewardMCPServer builds the ACP mcpServers entry for the steward: this server's own gofer
// binary (the one that issued the credential) running `mcp` as a stdio client of this hub,
// with an explicit env — the steward credential, the hub address, the job id and the
// steward marker — instead of whatever the agent happens to pass on.
//
// ok=false when the pieces are not there (no credential minted: the job simply runs without
// a gofer MCP rather than with a half-working one).
func stewardMCPServer(env map[string]string) (runner.ACPMCPServer, bool) {
	tok := env[EnvJobToken]
	if tok == "" {
		return runner.ACPMCPServer{}, false
	}
	bin := env[EnvJobBin]
	if bin == "" {
		exe, err := os.Executable()
		if err != nil || exe == "" {
			return runner.ACPMCPServer{}, false
		}
		bin = exe
	}
	mcpEnv := map[string]string{
		EnvJobToken:     tok,
		goferStewardEnv: "1",
	}
	if addr := env[EnvServerAddr]; addr != "" {
		mcpEnv[EnvServerAddr] = addr
	}
	if id := env["GOFER_JOB_ID"]; id != "" {
		mcpEnv["GOFER_JOB_ID"] = id
	}
	return runner.ACPMCPServer{Name: stewardMCPName, Command: bin, Args: []string{"mcp"}, Env: mcpEnv}, true
}

// applyStewardRun stamps a steward job's run request: the steward marker in its env and
// the gofer MCP in its ACP session. It replaces a same-named server the agent's own
// config might have listed, so the steward's gofer is always the narrowed one.
func applyStewardRun(runReq *runner.Request) error {
	if runReq.ACP == nil {
		return fmt.Errorf("steward job needs an acp-agent")
	}
	runReq.Env = util.EnvWith(runReq.Env, map[string]string{goferStewardEnv: "1"})
	srv, ok := stewardMCPServer(runReq.Env)
	if !ok {
		return nil
	}
	kept := runReq.ACP.MCPServers[:0:0]
	for _, m := range runReq.ACP.MCPServers {
		if m.Name != stewardMCPName {
			kept = append(kept, m)
		}
	}
	runReq.ACP.MCPServers = append(kept, srv)
	return nil
}
