package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/mcpserver"
)

// mcpExitErr is the process exit code used when the mcp server fails to start or
// run. gcli derives the exit code from errorx.ErrorCoder errors (mirrors
// serveExitErr).
const mcpExitErr = 2

// mcpOpts holds `mcp`-specific flags. --standalone is the D2 escape hatch that
// forces in-process (local backend) mode even when GOFER_SERVER_ADDR/--server
// is set. The --server/--token connection flags live in the shared jobConnOpts
// (bound via bindServerFlags).
var mcpOpts = struct {
	standalone bool
	// project scopes the MCP server to one project (xu64.15). Three states:
	// "" = operator (all projects, 向后兼容), "<key>" = explicit scope,
	// "auto" = resolve from CWD (.gofer.project.yaml key: → standalone
	// ProjectForPath → GOFER_PROJECT env; error if none).
	project string
}{}

// NewMcpCmd builds the `mcp` command: run the stdio MCP server (plan P8/E28).
// Two backend modes (E28 D1/D2):
//   - client mode (default when --server/GOFER_SERVER_ADDR is set, no
//     --standalone): the mcp process is a thin client — the 10 gofer_* tools
//     forward to a central serve via internal/client. NO Core/DB is built.
//   - standalone mode (--standalone, or no server addr resolved): the現状
//     in-process path — load config + build Core + serve the localBackend,
//     identical to `serve` so the MCP tools never duplicate execution logic.
//
// IMPORTANT: the MCP protocol owns stdout (newline-delimited JSON over stdin/
// stdout), so this command MUST NOT print anything to stdout — no startup
// banner, no logs, in EITHER mode. Errors surface via the returned (coded)
// error on stderr (finishMcp).
func NewMcpCmd() *gcli.Command {
	return &gcli.Command{
		Name: "mcp",
		Desc: "Run the stdio MCP server",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c) // --server/-s + --token (GOFER_SERVER_ADDR/TOKEN env defaults)
			c.BoolOpt(&mcpOpts.standalone, "standalone", "", false,
				"force in-process mode (ignore GOFER_SERVER_ADDR / --server)")
			c.StrOpt(&mcpOpts.project, "project", "", "",
				"scope MCP to one project: <key> | auto (CWD/env) | empty=operator(all)")
		},
		Func: runMcp,
	}
}

// mcpUseClient reports whether the mcp server should run in client mode (E28
// D1/D2). It is a pure function of the resolved flag/env inputs so the mode
// decision is unit-testable. serverAddr is the value bound by bindServerFlags
// (jobConnOpts.server) — i.e. --server flag OR the GOFER_SERVER_ADDR env default
// — and deliberately NOT config.server.addr: config.server.addr is the
// standalone serve's own listen address, so honouring it here would make a serve
// host's bare `gofer mcp` connect back to itself (D1).
func mcpUseClient(standalone bool, serverAddr string) bool {
	return !standalone && serverAddr != ""
}

// resolveScopedProject maps the --project flag's three states to a scoped project
// key (xu64.15). It is a pure function (cfg may be nil for the client/thin path)
// so the resolution truth table is unit-testable. An empty flag → ("", nil) =
// operator (all projects, 向后兼容). "auto" resolves by precedence: the CWD's
// .gofer.project.yaml key: (no round trip) → standalone ProjectForPath (cfg != nil)
// → GOFER_PROJECT env → error (never silently fall back to operator).
func resolveScopedProject(flag string, cfg *config.Config, cwd string) (string, error) {
	if flag == "" {
		return "", nil // operator (向后兼容)
	}
	if flag != "auto" {
		return flag, nil // explicit --project <key>
	}
	if k, err := config.ProjectKeyFromDir(cwd); err != nil {
		return "", err
	} else if k != "" {
		return k, nil
	}
	if cfg != nil {
		if k, ok := cfg.ProjectForPath(cwd); ok {
			return k, nil
		}
	}
	if k := strings.TrimSpace(os.Getenv("GOFER_PROJECT")); k != "" {
		return k, nil
	}
	return "", fmt.Errorf("--project auto: 无法从 CWD(%s) / .gofer.project.yaml / GOFER_PROJECT 解析 project", cwd)
}

func runMcp(_ *gcli.Command, _ []string) error {
	cwd, _ := os.Getwd()

	// D1: client mode — thin client forwarding to a central serve. No Core/DB is
	// built (root-causes the standalone multi-process SQLite write-lock risk).
	if mcpUseClient(mcpOpts.standalone, jobConnOpts.server) {
		cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
		if err != nil {
			return errorx.Failf(mcpExitErr, "%v", err)
		}
		// client (thin) has no full cfg → nil; --project auto resolves via the CWD
		// overlay key: or GOFER_PROJECT only (no ProjectForPath).
		scoped, err := resolveScopedProject(mcpOpts.project, nil, cwd)
		if err != nil {
			return errorx.Failf(mcpExitErr, "%v", err)
		}
		return finishMcp(mcpserver.Serve(context.Background(), mcpserver.NewClientBackend(cli), scoped))
	}

	// D2/standalone (現状路径，operator 时行为不变): load config + build Core + serve
	// the in-process localBackend.
	cfg, _, err := config.Load(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(mcpExitErr, "%v", err)
	}
	// Merge per-project thin overlays before Core (D6). MCP owns stdout (protocol
	// channel), so overlay warns MUST go to stderr — never stdout.
	for _, w := range config.ApplyProjectOverlays(cfg) {
		fmt.Fprintf(os.Stderr, "gofer mcp: overlay warn: %s\n", w)
	}
	// Resolve scope before Core so a bad --project auto fails fast (no wasted build).
	scoped, err := resolveScopedProject(mcpOpts.project, cfg, cwd)
	if err != nil {
		return errorx.Failf(mcpExitErr, "%v", err)
	}
	cr, err := core.Build(cfg)
	if err != nil {
		return errorx.Failf(mcpExitErr, "%v", err)
	}
	// Graceful shutdown: close the metadata store (WAL checkpoint) when the MCP
	// server returns (design §14).
	defer func() { _ = cr.Close() }()
	return finishMcp(mcpserver.ServeLocal(context.Background(), cr.Jobs, cr.Projects, cr.Agents, cr.Presence, scoped))
}

// finishMcp maps the mcp server's return error onto the command result, shared by
// both the client and standalone paths so stdout stays clean either way. A clean
// stdin EOF (client closed the pipe) or a cancelled context is the normal
// stdio-MCP shutdown path, not a failure — returning the error would make gcli
// render "ERROR: ..." onto stdout, which is the MCP protocol channel and must
// stay clean — so we swallow it and exit 0. A real failure surfaces as a coded
// error on stderr.
func finishMcp(err error) error {
	if isCleanShutdown(err) {
		return nil
	}
	return errorx.Failf(mcpExitErr, "%v", err)
}

// isCleanShutdown reports whether err is the normal stdio-MCP teardown signal
// rather than a real failure. The SDK surfaces stdin close as either a plain
// io.EOF (errors.Is) or a jsonrpc2 "server is closing: EOF" wire error whose
// type lives in an internal package; the latter is matched by message so we do
// not depend on an unexported type.
func isCleanShutdown(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "server is closing") || strings.HasSuffix(msg, "EOF")
}
