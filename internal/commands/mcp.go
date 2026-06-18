package commands

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/mcpserver"
)

// mcpExitErr is the process exit code used when the mcp server fails to start or
// run. gcli derives the exit code from errorx.ErrorCoder errors (mirrors
// serveExitErr).
const mcpExitErr = 2

// mcpOpts holds the mcp command flags.
var mcpOpts = struct {
	config string
}{}

// NewMcpCmd builds the `mcp` command: load config, wire the shared Core and run
// the stdio MCP server (plan P8). It reuses the identical job.Service / project
// / agent registries as `serve` so the MCP tools never duplicate execution
// logic.
//
// IMPORTANT: the MCP protocol owns stdout (newline-delimited JSON over stdin/
// stdout), so this command MUST NOT print anything to stdout — no startup
// banner, no logs. Errors surface via the returned (coded) error on stderr.
func NewMcpCmd() *gcli.Command {
	return &gcli.Command{
		Name: "mcp",
		Desc: "Run the stdio MCP server",
		Config: func(c *gcli.Command) {
			c.StrOpt(&mcpOpts.config, "config", "c", "", "path to the bridge config file")
		},
		Func: runMcp,
	}
}

func runMcp(_ *gcli.Command, _ []string) error {
	cfg, _, err := config.Load(mcpOpts.config)
	if err != nil {
		return errorx.Failf(mcpExitErr, "%v", err)
	}
	core, err := buildCore(cfg)
	if err != nil {
		return errorx.Failf(mcpExitErr, "%v", err)
	}
	// Graceful shutdown: close the metadata store (WAL checkpoint) when the MCP
	// server returns (design §14).
	defer func() { _ = core.Close() }()
	err = mcpserver.Serve(context.Background(), core.Jobs, core.Projects, core.Agents)
	if isCleanShutdown(err) {
		// A clean stdin EOF (client closed the pipe) or a cancelled context is the
		// normal stdio-MCP shutdown path, not a failure. Returning the error here
		// would make gcli render "ERROR: ..." onto stdout — which is the MCP
		// protocol channel and must stay clean — so we swallow it and exit 0.
		return nil
	}
	if err != nil {
		return errorx.Failf(mcpExitErr, "%v", err)
	}
	return nil
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
