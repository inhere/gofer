// mcp-call is a minimal one-shot MCP stdio client for the decision-channel
// smoke (scripts/smoke/decision-channel). It spawns `gofer mcp` (client mode,
// forwarding to an isolated central serve), performs the MCP handshake, calls
// ONE tool, prints the raw CallToolResult JSON to stdout and exits:
//
//	0 = tool returned a normal result (inspect the JSON)
//	2 = tool returned isError=true
//	1 = protocol/transport/deadline failure
//
// It exists because the smoke must drive a REAL `gofer mcp` subprocess over
// stdio (the deployment shape agents actually use), not an in-memory transport.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	var (
		goferBin = flag.String("gofer", "", "path to the gofer binary")
		addr     = flag.String("server", "", "central serve addr (client mode)")
		token    = flag.String("token", "", "bearer token")
		tool     = flag.String("tool", "", "tool name to call")
		argsJSON = flag.String("args", "{}", "tool arguments as JSON object")
		deadline = flag.Duration("deadline", 0, "overall deadline incl. the tool call (0 = none)")
	)
	flag.Parse()
	if *goferBin == "" || *tool == "" {
		fmt.Fprintln(os.Stderr, "mcp-call: -gofer and -tool are required")
		os.Exit(1)
	}
	args := map[string]any{}
	if err := json.Unmarshal([]byte(*argsJSON), &args); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-call: bad -args JSON: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if *deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *deadline)
		defer cancel()
	}

	// Client mode: --server points the mcp subprocess at the isolated central
	// serve (commands/mcp.go D1). Stderr is inherited (diagnostics); stdout is
	// the protocol channel and must stay clean.
	cmd := exec.CommandContext(ctx, *goferBin, "mcp", "--server", *addr, "--token", *token)
	cmd.Stderr = os.Stderr
	cli := mcp.NewClient(&mcp.Implementation{Name: "mcp-call", Version: "0"}, nil)
	session, err := cli.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-call: connect: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: *tool, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-call: call %s: %v\n", *tool, err)
		os.Exit(1)
	}
	out, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-call: marshal result: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
	if res.IsError {
		os.Exit(2)
	}
}
