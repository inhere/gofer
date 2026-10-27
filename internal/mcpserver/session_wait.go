package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Session wait / screen (gofer-f8ik): what an agent needs to orchestrate ANOTHER
// session — block until it is free (or asks something, or is gone) and read what it
// shows — without guessing from `gofer_session_list` and sleeping.

type sessionWaitInput struct {
	// ID is the session id (a unique prefix is not resolved here: the caller reads
	// the full id from gofer_session_list).
	ID string `json:"id"`
	// Until is the state to wait for: idle | waiting_reply | ended. Default idle.
	Until string `json:"until,omitempty"`
	// TimeoutSec bounds the wait (default 120s, at most 600s). A timeout is not an
	// error: the answer says reached=false and carries the state it found.
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

type sessionScreenInput struct {
	ID string `json:"id"`
	// N is how many lines to return (default 50, at most 500).
	N int `json:"n,omitempty"`
}

// sessionWaitDefaults bound one MCP wait: an agent call must not park a tool for
// as long as a person's `gofer session wait --timeout 10m` may.
const (
	defaultMCPWaitSec = 120
	maxMCPWaitSec     = 600
)

func registerSessionWaitTools(s *mcp.Server, b Backend) {
	addGoferTool(s, &mcp.Tool{
		Name: "gofer_session_wait",
		Description: "Block until another session reaches a state (read-only): until is idle (it is free — default), waiting_reply (it asks the human something) or ended (it is gone); " +
			"a comma-separated list waits for any of them. Returns the session and reached=false when timeout_sec ran out — a timeout is an outcome, not an error. " +
			"Already being in that state returns at once. Needs a running gofer server.",
	}, sessionWaitHandler(b))
	addGoferTool(s, &mcp.Tool{
		Name: "gofer_session_screen",
		Description: "Read the tail of what another session shows right now (read-only): its recent relay turns (agent question / human answer, the same rows gofer_session_get returns) " +
			"or, for a session taken over by an interactive pty job, that job's terminal transcript. Text has ANSI escapes removed; n bounds the lines (default 50). Never writes to the session.",
	}, sessionScreenHandler(b))
}

func sessionWaitHandler(b Backend) mcp.ToolHandlerFor[sessionWaitInput, client.SessionWaitResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in sessionWaitInput) (*mcp.CallToolResult, client.SessionWaitResult, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, client.SessionWaitResult{}, fmt.Errorf("id is required")
		}
		until := strings.ToLower(strings.TrimSpace(in.Until))
		if until == "" {
			until = "idle"
		}
		states := strings.Split(until, ",")
		timeout := in.TimeoutSec
		if timeout <= 0 {
			timeout = defaultMCPWaitSec
		}
		timeout = min(timeout, maxMCPWaitSec)
		res, err := b.WaitSession(id, states, time.Duration(timeout)*time.Second)
		return nil, res, err
	}
}

func sessionScreenHandler(b Backend) mcp.ToolHandlerFor[sessionScreenInput, client.SessionScreen] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in sessionScreenInput) (*mcp.CallToolResult, client.SessionScreen, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, client.SessionScreen{}, fmt.Errorf("id is required")
		}
		res, err := b.SessionScreen(id, in.N)
		return nil, res, err
	}
}
